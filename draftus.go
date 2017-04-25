package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

////////////////////////////////////////////////////////////////

const commandPrefix = "?draft"

type command struct {
	name    string
	args    string
	execute func(string, *discordgo.Session, *discordgo.MessageCreate)
	help    string
}

var (
	// Note: we don't initialize commands here in order to avoid an initialization loop

	commandHelp   command
	commandStart  command
	commandAbort  command
	commandAdd    command
	commandRemove command
	commandWho    command
	commandClose  command
	commandPick   command

	commandList = [...]*command{
		&commandHelp,
		&commandStart,
		&commandAbort,
		&commandAdd,
		&commandRemove,
		&commandWho,
		&commandClose,
		&commandPick,
	}
)

func (cmd *command) syntax() string {
	return commandPrefix + " " + cmd.name + cmd.args
}

func (cmd *command) syntaxNoArgs() string {
	return commandPrefix + " " + cmd.name
}

func (cmd *command) syntaxLength() int {
	return len(commandPrefix) + 1 + len(cmd.name) + len(cmd.args)
}

////////////////////////////////////////////////////////////////

// Cup status
const (
	Inactive = iota
	Signup   = iota
	Pickup   = iota
)

// Player counts
const (
	TeamSize       = 4
	MinimumTeams   = 2
	MinimumPlayers = TeamSize * MinimumTeams
)

// Cup report fields
const (
	CupReportTeams      = 1 << iota
	CupReportPlayers    = 1 << iota
	CupReportSubs       = 1 << iota
	CupReportNextAction = 1 << iota

	CupReportAll = -1
)

type (
	player struct {
		name string
		id   string
		team int
		next int
	}

	team struct {
		first     int
		last      int
		nameIndex int
	}

	pickupSlot struct {
		team   int
		player int
	}

	cup struct {
		status                 int
		pickedPlayers          int
		longestTeamName        int // for nicer string formatting
		longestTeamDescription int // ditto
		manager                player
		players                []player
		teams                  []team
		channelID              string
		guildID                string
		lastSpamID             string
		description            string
	}
)

var (
	lockCups   sync.Mutex
	activeCups = make(map[string]*cup)
	done       = make(chan bool)
)

////////////////////////////////////////////////////////////////

func makePlayer(user *discordgo.User) player {
	return player{user.Username, user.ID, -1, -1}
}

func (currentTeam *team) resetTeam() {
	currentTeam.first = -1
	currentTeam.last = -1
	currentTeam.nameIndex = -1
}

func (currentTeam *team) getName() string {
	attrib, noun := decomposeName(currentTeam.nameIndex)
	return Attributes[attrib] + " " + Nouns[noun]
}

func (currentTeam *team) getNameLength() int {
	attrib, noun := decomposeName(currentTeam.nameIndex)
	return len(Attributes[attrib]) + 1 + len(Nouns[noun])
}

////////////////////////////////////////////////////////////////

func getCup(channelID string) *cup {
	lockCups.Lock()
	currentCup := activeCups[channelID]
	lockCups.Unlock()
	return currentCup
}

func addCup(channelID string) *cup {
	currentCup := new(cup)
	currentCup.status = Signup
	currentCup.channelID = channelID

	lockCups.Lock()
	activeCups[channelID] = currentCup
	lockCups.Unlock()

	return currentCup
}

func deleteCup(channelID string) {
	lockCups.Lock()
	delete(activeCups, channelID)
	lockCups.Unlock()
}

func (currentCup *cup) findPlayer(id string) int {
	for i := range currentCup.players {
		if currentCup.players[i].id == id {
			return i
		}
	}
	return -1
}

// Returns the nth player in the list of active players
// that hasn't been assigned to a team yet, or -1 if none.
// Note: subs are not taken into consideration
func (currentCup *cup) findAvailablePlayer(nth int) int {
	numActive := currentCup.activePlayerCount()
	if nth < 0 || nth > numActive {
		return -1
	}
	for i := 0; i < numActive; i++ {
		player := &currentCup.players[i]
		if player.team == -1 {
			if nth == 0 {
				return i
			}
			nth--
		}
	}
	return -1
}

func (currentCup *cup) nextAvailablePlayer() int {
	return currentCup.findAvailablePlayer(0)
}

func (currentCup *cup) isSuperUser(id string) bool {
	return currentCup.status != Inactive && currentCup.manager.id == id
}

func (currentCup *cup) targetPlayerCount() int {
	target := len(currentCup.players)
	target += TeamSize - 1
	target -= target % TeamSize
	if target < MinimumPlayers {
		target = MinimumPlayers
	}
	return target
}

func (currentCup *cup) activePlayerCount() int {
	return len(currentCup.teams) * TeamSize
}

func (currentCup *cup) currentPickup() pickupSlot {
	nthPlayer := currentCup.pickedPlayers / len(currentCup.teams)
	nthTeam := currentCup.pickedPlayers % len(currentCup.teams)

	// First round is for picking captains, which is done in order.
	// The second round is for captains making their first pick, which also happens in order.
	// For rounds 3 and 4, picking order is reversed in order to better balance the teams.
	if nthPlayer >= 2 && nthPlayer <= 3 {
		nthTeam = len(currentCup.teams) - 1 - nthTeam
	}

	return pickupSlot{nthTeam, nthPlayer}
}

func (currentCup *cup) whoPicks(pickup pickupSlot) *player {
	if currentCup.status != Pickup {
		return nil
	}
	if pickup.player < 0 || pickup.player >= TeamSize {
		return nil
	}
	if pickup.team < 0 || pickup.team >= len(currentCup.teams) {
		return nil
	}
	if pickup.player == 0 {
		return &currentCup.manager
	}
	index := currentCup.teams[pickup.team].first
	if index < 0 || index >= len(currentCup.players) {
		return nil
	}
	return &currentCup.players[index]
}

func (currentCup *cup) chooseTeamNames() {
	// Re-seed RNG
	rand.Seed(time.Now().UTC().UnixNano())

	currentCup.longestTeamName = 0
	currentCup.longestTeamDescription = 0

	for i := 0; i < len(currentCup.teams); i++ {
		currentTeam := &currentCup.teams[i]

		for retry := 0; retry < 100; retry++ {
			currentTeam.nameIndex = rand.Intn(TeamNameCombos)
			attrib, noun := decomposeName(currentTeam.nameIndex)
			found := false
			for j := 0; j < i; j++ {
				otherTeam := &currentCup.teams[j]
				otherAttrib, otherNoun := decomposeName(otherTeam.nameIndex)
				if attrib == otherAttrib || noun == otherNoun {
					found = true
					break
				}
			}
			if !found {
				break
			}
		}

		length := currentTeam.getNameLength()
		if length > currentCup.longestTeamName {
			currentCup.longestTeamName = length
		}

		indexDigits := 1
		for j := i; j >= 10; j /= 10 {
			indexDigits++
		}

		length += indexDigits + 2 // number, dot, space
		if length > currentCup.longestTeamDescription {
			currentCup.longestTeamDescription = length
		}
	}
}

// Returns formatted join message or an error
func (currentCup *cup) addPlayerToTeam(playerIndex int, teamIndex int) (string, error) {
	if playerIndex < 0 || playerIndex >= len(currentCup.players) {
		return "", fmt.Errorf("player index out of range: %d", playerIndex)
	}
	if teamIndex < 0 || teamIndex >= len(currentCup.teams) {
		return "", fmt.Errorf("team index out of range: %d", teamIndex)
	}

	player := &currentCup.players[playerIndex]
	if player.team != -1 {
		return "", fmt.Errorf("already assigned to %d", player.team)
	}

	player.team = teamIndex
	team := &currentCup.teams[teamIndex]
	if team.first == -1 {
		team.first = playerIndex
		team.last = playerIndex
	} else {
		lastPlayer := &currentCup.players[team.last]
		lastPlayer.next = playerIndex
		team.last = playerIndex
	}

	currentCup.pickedPlayers++

	message := bold(player.name) + " joined team " + strconv.Itoa(teamIndex+1) + ", " + bold(currentCup.teams[teamIndex].getName())
	if team.first == playerIndex {
		message += " (as captain)"
	}

	return message + ".\n", nil
}

func (currentCup *cup) getLineup(index int) (string, error) {
	if index < 0 || index >= len(currentCup.teams) {
		return "", fmt.Errorf("index out of range: %d", index)
	}
	team := &currentCup.teams[index]
	lineup := ""
	for playerIndex, count := team.first, 0; playerIndex != -1; count++ {
		player := &currentCup.players[playerIndex]
		if count != 0 {
			lineup += ", "
		}
		lineup += player.name
		playerIndex = player.next
	}
	return lineup, nil
}

func (currentCup *cup) report(selector int) string {
	message := ""

	switch currentCup.status {
	case Signup:
		if (selector & CupReportPlayers) != 0 {
			if len(currentCup.players) == 0 {
				message += "No players signed up for the cup so far.\n"
				if (selector & CupReportNextAction) != 0 {
					message += "You can be the first by typing " + bold(commandAdd.syntax()) + "\n"
				}
			} else {
				message += numbered(len(currentCup.players), "player") + " signed up so far:\n```"
				for i := range currentCup.players {
					message += strconv.Itoa(i+1) + ". " + currentCup.players[i].name + "\n"
				}
				message += "```\n"
			}
		}
		if (selector & CupReportNextAction) != 0 {
			message += "Sign up now by typing " + bold(commandAdd.syntax()) + "\n"
		}

	case Pickup:
		active := currentCup.activePlayerCount()
		if (selector & CupReportTeams) != 0 {
			if currentCup.pickedPlayers != active {
				message += fmt.Sprintf("%d/%d players picked, competing in %d teams:\n```\n", currentCup.pickedPlayers, active, len(currentCup.teams))
			} else {
				message += fmt.Sprintf("%d players, competing in %d teams:\n```\n", active, len(currentCup.teams))
			}
			for i := range currentCup.teams {
				lineup, _ := currentCup.getLineup(i)
				teamDescription := strconv.Itoa(i+1) + ". " + currentCup.teams[i].getName()
				message += fmt.Sprintf("%*s : %s\n", -currentCup.longestTeamDescription, teamDescription, lineup)
			}
			message += "```\n"
		}

		if (selector & CupReportPlayers) != 0 {
			unpicked := active - currentCup.pickedPlayers
			if unpicked > 0 {
				message += strconv.Itoa(unpicked) + " available players:\n```\n"
				for i := 0; i < active; i++ {
					player := &currentCup.players[i]
					if player.team != -1 {
						continue
					}
					message += strconv.Itoa(i+1) + ". " + player.name + "\n"
				}
				message += "\n```\n"
			}
		}

		if (selector & CupReportSubs) != 0 {
			subs := len(currentCup.players) - active
			if subs > 0 {
				message += numbered(subs, " substitute player") + ":\n```\n"
				for i := active; i < len(currentCup.players); i++ {
					player := &currentCup.players[i]
					message += strconv.Itoa(i+1) + ". " + player.name + "\n"
				}
				message += "\n```\n"
			}
		}

		if (selector & CupReportNextAction) != 0 {
			pickup := currentCup.currentPickup()
			who := currentCup.whoPicks(pickup)

			if who != nil {
				teamName := currentCup.teams[pickup.team].getName()
				teamDescription := "team " + strconv.Itoa(pickup.team+1) + ", " + bold(teamName)

				if pickup.player == 0 {
					message += bold(who.name) + ", pick a captain for " + teamDescription + ", by typing " + bold(commandPick.syntax()) + "\n"
				} else {
					message += bold(who.name) + ", pick player " + strconv.Itoa(pickup.player+1) + " for " + teamDescription + ", by typing " + bold(commandPick.syntax()) + "\n"
				}
			} else {
				message += "Good luck and have fun!\n"
			}
		}
	}

	return message
}

func (currentCup *cup) removeLastSpam(s *discordgo.Session) {
	if len(currentCup.lastSpamID) > 0 {
		s.ChannelMessageDelete(currentCup.channelID, currentCup.lastSpamID)
		currentCup.lastSpamID = ""
	}
}

func (currentCup *cup) addSpam(s *discordgo.Session, text string) {
	currentCup.removeLastSpam(s)
	message, err := s.ChannelMessageSend(currentCup.channelID, text)
	if err == nil {
		currentCup.lastSpamID = message.ID
	}
}

////////////////////////////////////////////////////////////////

func numbered(count int, singular string) string {
	result := strconv.Itoa(count) + " " + singular
	if count != 1 {
		result += "s"
	}
	return result
}

func bold(s string) string {
	return "**" + s + "**"
}

func italic(s string) string {
	return "*" + s + "*"
}

func bolditalic(s string) string {
	return "***" + s + "***"
}

func mention(who *player) string {
	return "<@" + who.id + ">"
}

////////////////////////////////////////////////////////////////

func parseToken(cmd string) (string, string) {
	separators := " \t\n\r"
	splitPoint := strings.IndexAny(cmd, separators)
	if splitPoint == -1 {
		return cmd, ""
	}

	token := cmd[:splitPoint]
	for splitPoint++; splitPoint < len(cmd); splitPoint++ {
		if strings.IndexByte(separators, cmd[splitPoint]) == -1 {
			break
		}
	}

	return token, cmd[splitPoint:]
}

////////////////////////////////////////////////////////////////

// This function will be called (due to AddHandler above) every time a new
// message is created on any channel that the autenticated bot has access to.
func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore all messages created by the bot itself
	if m.Author.ID == BotID {
		return
	}

	if len(m.Content) < len(commandPrefix) {
		return
	}

	prefix := strings.ToLower(m.Content[:len(commandPrefix)])
	if prefix != commandPrefix {
		return
	}

	command := m.Content[len(commandPrefix):]
	command = strings.TrimSpace(command)

	var token string
	token, command = parseToken(command)

	if len(token) == 0 {
		commandHelp.execute("", s, m)
		return
	}

	token = strings.ToLower(token)

	for _, cmd := range commandList {
		if cmd.name == token {
			cmd.execute(command, s, m)
			return
		}
	}

	_, _ = s.ChannelMessageSend(m.ChannelID, "Unknown command, '"+token+"'.\n")
	commandHelp.execute("", s, m)
}

var (
	index = 1
)

func handleStart(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup != nil {
		var message string
		if currentCup.manager.id == m.Author.ID {
			message = "You"
		} else {
			message = bold(currentCup.manager.name)
		}
		message += " already started the cup"

		if currentCup.status == Signup {
			if currentCup.findPlayer(m.Author.ID) == -1 {
				message += ", you can sign up with " + bold(commandAdd.syntax())
			} else {
				message += ", try finding more players to sign up with " + bold(commandAdd.syntax())
			}
		} else {
			message += "."
		}

		_, _ = s.ChannelMessageSend(m.ChannelID, message)
		return
	}

	currentCup = addCup(m.ChannelID)
	currentCup.manager = makePlayer(m.Author)
	currentCup.description = args

	channel, err := s.Channel(m.ChannelID)
	if err != nil {
		fmt.Println("Could not retrieve channel info:", err.Error())
	} else {
		currentCup.guildID = channel.GuildID
	}

	// Just a note to self on retrieving the list of roles for a given user
	if false {
		member, err := s.GuildMember(channel.GuildID, m.Author.ID)
		if err != nil {
			return
		}

		for _, roleID := range member.Roles {
			role, err := s.State.Role(channel.GuildID, roleID)
			if err == nil {
				fmt.Printf("%s: %s\n", m.Author.Username, role.Name)
			}
		}
	}

	message := "Hey, @everyone!\n\nRegistration is now open for a new draft cup, managed by " + bold(m.Author.Username) + ".\n\n"
	if len(args) > 0 {
		message += args + "\n\n"
	}
	message += "You can sign up now by typing " + bold(commandAdd.syntax())

	_, err = s.ChannelMessageSend(m.ChannelID, message)
	if err != nil {
		fmt.Println("Unable to send cup start message, aborting cup: ", err)
		deleteCup(m.ChannelID)
	}
}

func handleAbort(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Can't abort a cup that hasn't started.")
		return
	}

	if !currentCup.isSuperUser(m.Author.ID) {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Only **"+currentCup.manager.name+"**, the cup manager, can abort it.")
		return
	}

	_, _ = s.ChannelMessageSend(m.ChannelID, "Cup aborted. You can start a new one with "+bold(commandStart.syntax()))
	deleteCup(m.ChannelID)
}

func handleAdd(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil || currentCup.status == Inactive {
		_, _ = s.ChannelMessageSend(m.ChannelID, "No cup in progress. Try starting one with "+bold(commandStart.syntax()))
		return
	}

	switch currentCup.status {
	case Signup:
		before := currentCup.findPlayer(m.Author.ID)
		if before != -1 && !activeHacks.disableAlreadySignedUpCheck {
			_, _ = s.ChannelMessageSend(m.ChannelID, bold(m.Author.Username)+", you're already registered for this cup ("+strconv.Itoa(before+1)+"/"+strconv.Itoa(len(currentCup.players))+").")
		} else {
			currentCup.players = append(currentCup.players, makePlayer(m.Author))
			currentCup.removeLastSpam(s)
			_, _ = s.ChannelMessageSend(m.ChannelID, bold(m.Author.Username)+" joined the cup.\n")
			currentCup.addSpam(s, "\n"+currentCup.report(CupReportPlayers|CupReportNextAction))
		}
	default:
		_, _ = s.ChannelMessageSend(m.ChannelID, "Sorry, **"+m.Author.Username+"**, cup is no longer open for signup.")
	}
}

func handleRemove(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil {
		_, _ = s.ChannelMessageSend(m.ChannelID, "No cup in progress, anyway.")
		return
	}

	switch currentCup.status {
	case Signup:
		if len(currentCup.players) == 0 {
			_, _ = s.ChannelMessageSend(m.ChannelID, "No players to remove, nobody has signed up for the cup yet.")
			return
		}

		var which int
		var token string
		token, args = parseToken(args)
		if len(token) > 0 {
			if !currentCup.isSuperUser(m.Author.ID) {
				message := "Only the cup manager, **" + currentCup.manager.name + "**, can remove other players.\n"
				if currentCup.findPlayer(m.Author.ID) != -1 {
					message += "You can remove yourself by typing " + bold(commandRemove.syntaxNoArgs())
				}
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
				return
			}

			index, err := strconv.Atoi(token)
			if err != nil {
				message := bold(m.Author.Username) + ", '" + token + "' doesn't look like a number, either leave it out (to remove yourself from the list of players) or specify an actual player number.\n\n" +
					currentCup.report(CupReportPlayers)
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
				return
			}
			index-- // 0-based

			if index < 0 || index >= len(currentCup.players) {
				message := bold(m.Author.Username) + ", " + token + " is not a valid player number.\n\n" +
					currentCup.report(CupReportPlayers)
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
				return
			}

			which = index
		} else {
			which = currentCup.findPlayer(m.Author.ID)
			if which == -1 {
				_, _ = s.ChannelMessageSend(m.ChannelID, m.Author.Username+", you're not registered for this cup anyway.")
				return
			}
		}

		name := currentCup.players[which].name
		currentCup.players = append(currentCup.players[:which], currentCup.players[which+1:]...)
		_, _ = s.ChannelMessageSend(m.ChannelID, "Removed player **"+name+"** ("+strconv.Itoa(len(currentCup.players))+" remaining).")

	default:
		_, _ = s.ChannelMessageSend(m.ChannelID, "Cup is not currently open for signup, anyway.")
	}
}

func handleClose(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil {
		_, _ = s.ChannelMessageSend(m.ChannelID, "No cup in progress, no sign-ups to close.")
		return
	}

	switch currentCup.status {
	case Signup:
		if !currentCup.isSuperUser(m.Author.ID) {
			_, _ = s.ChannelMessageSend(m.ChannelID, "Only "+bold(currentCup.manager.name)+", the cup manager, can close it.")
			return
		}

		// Hack to allow testing
		if activeHacks.fillUpOnClose {
			if len(currentCup.players) == 0 {
				currentCup.players = append(currentCup.players, currentCup.manager)
			}
			for i := len(currentCup.players); i < 19; i++ {
				currentCup.players = append(currentCup.players, currentCup.players[0])
			}
		}

		signedUp := len(currentCup.players)
		if signedUp < MinimumPlayers {
			var who string
			if signedUp == 0 {
				who = "Nobody"
			} else {
				who = "Only " + numbered(signedUp, "player")
			}
			_, _ = s.ChannelMessageSend(m.ChannelID, who+" signed up, cup aborted.")
			deleteCup(m.ChannelID)
			return
		}

		var token string
		token, args = parseToken(args)
		if len(token) != 0 {
			count, err := strconv.Atoi(token)
			if err != nil {
				message := bold(m.Author.Username) + ", '" + token + "' doesn't look like a number, either leave it out or specify an actual number of players to keep.\n"
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
				return
			}
			if count < MinimumPlayers {
				message := bold(m.Author.Username) + ", you need at least " + strconv.Itoa(MinimumPlayers) + " players for the cup to start.\n"
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
				return
			}
			if count > signedUp {
				message := bold(m.Author.Username) + ", " + token + " players haven't signed up yet.\n"
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
				return
			}
			signedUp = count
		}

		numTeams := signedUp / TeamSize

		currentCup.status = Pickup
		currentCup.teams = make([]team, numTeams)
		for i := 0; i < numTeams; i++ {
			currentTeam := &currentCup.teams[i]
			currentTeam.resetTeam()
		}
		currentCup.chooseTeamNames()

		message := fmt.Sprintf("Cup registration now closed. The %d competing teams are:\n```\n", numTeams)
		for i := 0; i < numTeams; i++ {
			message += fmt.Sprintf("%d. %s\n", i+1, currentCup.teams[i].getName())
		}
		message += "```\n"
		message += currentCup.report(CupReportPlayers | CupReportSubs | CupReportNextAction)

		currentCup.removeLastSpam(s)

		_, _ = s.ChannelMessageSend(m.ChannelID, message)

	default:
		_, _ = s.ChannelMessageSend(m.ChannelID, "Too late, **"+m.Author.Username+"**, registration for this cup is already closed.")
	}
}

func handlePick(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil {
		_, _ = s.ChannelMessageSend(m.ChannelID, "No cup in progress. You can start one with "+bold(commandStart.syntax()))
		return
	}

	switch currentCup.status {
	case Signup:
		message := "**" + m.Author.Username + "**, we're not picking players yet.\n" +
			"Still waiting for everyone to register by typing " + bold(commandAdd.syntax())
		_, _ = s.ChannelMessageSend(m.ChannelID, message)
		return

	case Pickup:
		pickup := currentCup.currentPickup()
		who := currentCup.whoPicks(pickup)
		numActive := currentCup.activePlayerCount()

		if who == nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, m.Author.Username+", it's not your turn to pick.\n")
			return
		}

		if who.id != m.Author.ID {
			_, _ = s.ChannelMessageSend(m.ChannelID, m.Author.Username+", it's not your turn to pick, but "+bold(who.name)+"'s.\n")
			return
		}

		var token string
		token, args = parseToken(args)
		if len(token) == 0 {
			_, _ = s.ChannelMessageSend(m.ChannelID, bold(m.Author.Username)+", you need to specify a number from 1 to "+strconv.Itoa(numActive)+".")
			return
		}
		index, err := strconv.Atoi(token)
		if err != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, bold(m.Author.Username)+", '"+token+"' doesn't look like a number. You need to specify a number from 1 to "+strconv.Itoa(numActive)+".")
			return
		}
		index-- // 0-based

		if index < 0 || index >= len(currentCup.players) {
			currentCup.removeLastSpam(s)
			_, _ = s.ChannelMessageSend(m.ChannelID, bold(m.Author.Username)+", '"+token+"' is not a valid player number.\n")
			currentCup.addSpam(s, currentCup.report(CupReportPlayers|CupReportNextAction))
			return
		}

		if index >= numActive && index < len(currentCup.players) {
			sub := &currentCup.players[index]
			currentCup.removeLastSpam(s)
			_, _ = s.ChannelMessageSend(m.ChannelID, bold(m.Author.Username)+", you can't pick "+bold(sub.name)+", he's only registered as a substitute.\n")
			currentCup.addSpam(s, currentCup.report(CupReportPlayers|CupReportNextAction))
			return
		}

		selected := &currentCup.players[index]
		if selected.team != -1 {
			team := currentCup.teams[selected.team]
			message := bold(selected.name) + " already on team " + strconv.Itoa(selected.team+1) + ", " + bold(team.getName()) + ".\n"
			currentCup.removeLastSpam(s)
			_, _ = s.ChannelMessageSend(m.ChannelID, message)
			currentCup.addSpam(s, currentCup.report(CupReportPlayers|CupReportNextAction))
			return
		}

		text, _ := currentCup.addPlayerToTeam(index, pickup.team)

		// The last player isn't picked, but automatically assigned to the remaining slot.
		if currentCup.pickedPlayers == numActive-1 {
			currentCup.removeLastSpam(s)

			lastPlayer := currentCup.nextAvailablePlayer()
			lastSlot := currentCup.currentPickup()
			lastJoin, _ := currentCup.addPlayerToTeam(lastPlayer, lastSlot.team)
			text += lastJoin

			// We send the last two join messages separately, instead of merging them with the final report.
			// This way, the last two players to get picked aren't highlighted at the end if the report mentions @everyone.
			_, _ = s.ChannelMessageSend(m.ChannelID, text)

			// We unpin all our previously pinned messages
			allPinned, err := s.ChannelMessagesPinned(m.ChannelID)
			if err == nil {
				for _, pinnedMessage := range allPinned {
					if pinnedMessage.Author.ID == BotID {
						s.ChannelMessageUnpin(pinnedMessage.ChannelID, pinnedMessage.ID)
					}
				}
			}

			text = "Teams are now complete and the games can begin!\n" +
				bold(currentCup.manager.name) + " will take things from here, setting up matches and tracking scores.\n\n" +
				currentCup.report(CupReportTeams|CupReportSubs|CupReportNextAction) + "@everyone"

			lastMessage, err := s.ChannelMessageSend(m.ChannelID, text)
			if err == nil {
				s.ChannelMessagePin(lastMessage.ChannelID, lastMessage.ID)
			}

			deleteCup(m.ChannelID)
			return
		}

		currentCup.removeLastSpam(s)
		_, _ = s.ChannelMessageSend(m.ChannelID, text)
		currentCup.addSpam(s, "\n"+currentCup.report(CupReportPlayers|CupReportNextAction))

	default:
		_, _ = s.ChannelMessageSend(m.ChannelID, "Sorry, **"+m.Author.Username+"**, we're not picking players at this point.")
		return
	}
}

func handleWho(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil || currentCup.status == Inactive {
		_, _ = s.ChannelMessageSend(m.ChannelID, "No cup in progress. You can start one with "+bold(commandStart.syntax()))
		return
	}
	_, _ = s.ChannelMessageSend(m.ChannelID, currentCup.report(CupReportAll))
}

func handleHelp(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	maxSyntaxLength := 0
	for _, cmd := range commandList {
		length := cmd.syntaxLength()
		if length > maxSyntaxLength {
			maxSyntaxLength = length
		}
	}

	message := "Supported commands:\n```Note: arguments marked [] are optional, <> are mandatory.\n\n"
	for _, cmd := range commandList {
		message += fmt.Sprintf("%*s : %s\n", -maxSyntaxLength, cmd.syntax(), cmd.help)
	}
	message += "```\n"

	_, _ = s.ChannelMessageSend(m.ChannelID, message)
}

func decomposeName(index int) (int, int) {
	attribute := index % len(Attributes)
	noun := index / len(Attributes)
	return attribute, noun
}

// Random team names
var (
	Attributes = [...]string{
		"Black", "Grey", "Purple", "Brown", "Blue", "Red", "Green", "Magenta",
		"Loud", "Thundering", "Screaming", "Flaming", "Furious", "Zen", "Chill",
		"Laughing", "Giggly", "Unimpressed",
		"Inappropriate", "Indecent", "Sexy", "Hot", "Flirty", "Cheeky", "Cheesy",
		"Gangster", "Fugitive", "Outlaw", "Pirate", "Thug", "Kleptomaniac", "Killer", "Lethal",
		"Rookie", "Trained", "Tryhard", "Stronk",
		"Millenial", "Centennial",
		"Sprinting", "Strafing", "Crouching", "Rolling", "Dancing", "Standing", "Rising", "Camping", "Sniping", "Telefragging", "Warping",
		"Juggling",
		"Sleepy",
		"Tilted", "Excentric", "Irrational", "Claustrophobic",
		"Undercover", "Stealthy", "Hidden", "Obvious", "Deceptive",
		"Total",
		"Chocolate",
		"Plastic", "Metal", "Rubber", "Golden", "Paper",
		"Original", "Creative", "Articulate", "Elegant", "Polite", "Classy",
		"Retro", "Old-school", "Next-gen", "Revolutionary",
		"Punk", "Disco", "Electronic", "Analog",
		"Nerdy", "Trendy", "Sporty", "Chic",
		"Famous", "Incognito",
		"Slim", "Toned", "Muscular", "Round", "Heavy", "Well-fed", "Hungry", "Vegan",
		"Bearded", "Hairy", "Furry", "Fuzzy",
		"Fearless", "Fierce", "Heroic", "Unstoppable", "Lucky",
		"Polar", "Siberian", "Tropical", "Brazilian",
	}

	Nouns = [...]string{
		"Alligators", "Crocs",
		"Armadillos", "Beavers",
		"Bears", "Pandas",
		"Bunnies", "Hamsters", "Kittens", "Puppies", "Pitbulls",
		"Turtles",
		"Giraffes", "Gazelles",
		"Dolphins", "Sharks", "Piranhas", "Tunas", "Trouts",
		"Hornets",
		"Hippos", "Rhinos",
		"Tigers", "Cheetas", "Hyenas", "Dingos",
		"Baboons",
		"Hawks", "Eagles", "Ravens", "Pigeons", "Duckies", "Pterodactyls", "Dragons",
		"Ponies", "Zebras", "Stallions",
		"Zombies", "Unicorns", "Mermaids", "Trolls",
	}

	TeamNameCombos = len(Attributes) * len(Nouns)
)

////////////////////////////////////////////////////////////////

// Variables used for command line parameters
var (
	Token string
	BotID string
)

////////////////////////////////////////////////////////////////

func init() {
	flag.StringVar(&Token, "t", "", "Bot Token")
	flag.Parse()

	rand.Seed(time.Now().UTC().UnixNano())

	// Commands are initialized here to avoid initialization loop.

	commandHelp = command{
		"help",
		"",
		handleHelp,
		"Show this list",
	}
	commandStart = command{
		"start", " [message]",
		handleStart,
		"Start a new cup, with an optional description",
	}
	commandAbort = command{
		"abort", "",
		handleAbort,
		"Abort current cup",
	}
	commandAdd = command{
		"add", "",
		handleAdd,
		"Sign up to play in the cup",
	}
	commandRemove = command{
		"remove", " [number]",
		handleRemove,
		"Remove yourself from the cup (or another player, if admin)",
	}
	commandWho = command{
		"who", "",
		handleWho,
		"Show list of players in cup",
	}
	commandClose = command{
		"close", " [number]",
		handleClose,
		"Close cup for sign-ups, optionally keeping only [number] players",
	}
	commandPick = command{
		"pick", " <number>",
		handlePick,
		"Pick the player with the given number",
	}
}

func main() {
	// Create a new Discord session using the provided bot token.
	dg, err := discordgo.New("Bot " + Token)
	if err != nil {
		fmt.Println("error creating Discord session,", err)
		return
	}

	// Get the account information.
	u, err := dg.User("@me")
	if err != nil {
		fmt.Println("error obtaining account details,", err)
		return
	}

	// Store the account ID for later use.
	BotID = u.ID

	// Register messageCreate as a callback for the messageCreate events.
	dg.AddHandler(messageCreate)

	// Open the websocket and begin listening.
	err = dg.Open()
	if err != nil {
		fmt.Println("error opening connection,", err)
		return
	}
	defer dg.Close()

	// Update bot status, giving users a starting point.
	err = dg.UpdateStatus(0, "type "+commandPrefix)
	if err != nil {
		fmt.Println("error updating bot status,", err)
	}

	fmt.Println("Bot is now running. Press CTRL-C to exit.")

	// Intercept signals in order to shut down gracefully.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigs
		fmt.Println("Caught signal", sig)
		done <- true
	}()

	<-done

	fmt.Println("Bot stopped.")

	return
}

////////////////////////////////////////////////////////////////

type devHacks struct {
	fillUpOnClose               bool
	disableAlreadySignedUpCheck bool
}

// Developer hacks, for easier testing
var (
	activeHacks = devHacks{
		fillUpOnClose:               false,
		disableAlreadySignedUpCheck: false,
	}
)
