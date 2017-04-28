package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

////////////////////////////////////////////////////////////////

type commandGroup struct {
	description string
	prefix      string
	commands    []*command
}

type command struct {
	group   *commandGroup
	name    string
	args    string
	execute func(string, *discordgo.Session, *discordgo.MessageCreate)
	help    string
}

var (
	// Note: we don't initialize commands here in order to avoid an initialization loop

	commandHelp     command
	commandStart    command
	commandAbort    command
	commandAdd      command
	commandRemove   command
	commandWho      command
	commandModerate command
	commandClose    command
	commandPick     command
	commandPromote  command
	commandReopen   command

	draftCommands = commandGroup{
		prefix:      "?draft",
		description: "Draft commands",
		commands: []*command{
			&commandHelp,
			&commandStart,
			&commandAbort,
			&commandAdd,
			&commandRemove,
			&commandWho,
			&commandModerate,
			&commandClose,
			&commandPick,
			&commandPromote,
			&commandReopen,
		},
	}

	commandGroups = [...]*commandGroup{
		&draftCommands,
	}
)

func (cmd *command) syntax() string {
	return cmd.group.prefix + " " + cmd.name + cmd.args
}

func (cmd *command) syntaxNoArgs() string {
	return cmd.group.prefix + " " + cmd.name
}

func (cmd *command) syntaxLength() int {
	return len(cmd.group.prefix) + 1 + len(cmd.name) + len(cmd.args)
}

////////////////////////////////////////////////////////////////

// Cup status
const (
	CupStatusInactive = iota
	CupStatusSignup   = iota
	CupStatusPickup   = iota
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

// Minimum amount of time that has to pass between promotions
const (
	MinimumPromotionInterval        = time.Hour * 2
	MinimumPromotionIntervalManager = time.Minute * 15
)

type (
	// Player holds data for a signed up user
	Player struct {
		Name string
		ID   string
		Team int
		Next int
	}

	// Team holds data for an assembled team
	Team struct {
		First int
		Last  int
		Name  string

		nameIndex int // only used during initialization
	}

	pickupSlot struct {
		Team   int
		Player int
	}

	// Cup holds data for an ongoing event
	Cup struct {
		Status                 int
		Moderated              bool
		PickedPlayers          int
		Manager                Player
		Players                []Player
		Teams                  []Team
		ChannelID              string
		GuildID                string
		StartMessageID         string
		LastReplyID            string
		Description            string
		StartTime              time.Time
		NextPromoteTime        time.Time
		NextPromoteTimeManager time.Time

		longestTeamName        int // for nicer string formatting
		longestTeamDescription int // ditto
	}
)

var (
	lockCups   sync.Mutex
	activeCups = make(map[string]*Cup)
	done       = make(chan bool)
)

////////////////////////////////////////////////////////////////

func makePlayer(user *discordgo.User) Player {
	return Player{
		Name: user.Username,
		ID:   user.ID,
		Team: -1,
		Next: -1,
	}
}

func (player *Player) resetTeam() {
	player.Team = -1
	player.Next = -1
}

func (currentTeam *Team) resetTeam() {
	currentTeam.First = -1
	currentTeam.Last = -1
	currentTeam.Name = ""
	currentTeam.nameIndex = -1
}

////////////////////////////////////////////////////////////////

func getCup(channelID string) *Cup {
	lockCups.Lock()
	currentCup := activeCups[channelID]
	lockCups.Unlock()
	return currentCup
}

func addCup(channelID string) *Cup {
	currentCup := new(Cup)
	currentCup.Status = CupStatusSignup
	currentCup.ChannelID = channelID

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

func (currentCup *Cup) findPlayer(id string) int {
	for i := range currentCup.Players {
		if currentCup.Players[i].ID == id {
			return i
		}
	}
	return -1
}

// Returns the nth player in the list of active players
// that hasn't been assigned to a team yet, or -1 if none.
// Note: subs are not taken into consideration
func (currentCup *Cup) findAvailablePlayer(nth int) int {
	numActive := currentCup.activePlayerCount()
	if nth < 0 || nth > numActive {
		return -1
	}
	for i := 0; i < numActive; i++ {
		player := &currentCup.Players[i]
		if player.Team == -1 {
			if nth == 0 {
				return i
			}
			nth--
		}
	}
	return -1
}

func (currentCup *Cup) nextAvailablePlayer() int {
	return currentCup.findAvailablePlayer(0)
}

func (currentCup *Cup) isManager(id string) bool {
	return currentCup.Status != CupStatusInactive && currentCup.Manager.ID == id
}

func (currentCup *Cup) isSuperUser(id string) bool {
	// Check cup manager first
	if currentCup.isManager(id) {
		return true
	}

	// If not the manager, check for an appropriate role

	member, err := Session.GuildMember(currentCup.GuildID, id)
	if err != nil {
		fmt.Println("Error retrieving guild member:", err)
		return false
	}

	adminRoles := [...]string{
		"DraftusAdmin",
		"Admins",
		"Admin",
		"Supervisors",
		"Supervisor",
		"DraftCupOrganizer",
	}

	for _, roleID := range member.Roles {
		role, err := Session.State.Role(currentCup.GuildID, roleID)
		if err != nil {
			fmt.Println("Error retrieving role info:", err)
			continue
		}
		for _, adminRoleName := range adminRoles {
			if strings.EqualFold(role.Name, adminRoleName) {
				return true
			}
		}
	}

	return false
}

func (currentCup *Cup) targetPlayerCount() int {
	target := len(currentCup.Players)
	target += TeamSize - 1
	target -= target % TeamSize
	if target < MinimumPlayers {
		target = MinimumPlayers
	}
	return target
}

func (currentCup *Cup) activePlayerCount() int {
	return len(currentCup.Teams) * TeamSize
}

func (currentCup *Cup) currentPickup() pickupSlot {
	nthPlayer := currentCup.PickedPlayers / len(currentCup.Teams)
	nthTeam := currentCup.PickedPlayers % len(currentCup.Teams)

	// First round is for picking captains, which is done in order.
	// The second round is for captains making their first pick, which also happens in order.
	// For rounds 3 and 4, picking order is reversed in order to better balance the teams.
	if nthPlayer >= 2 && nthPlayer <= 3 {
		nthTeam = len(currentCup.Teams) - 1 - nthTeam
	}

	return pickupSlot{nthTeam, nthPlayer}
}

func (currentCup *Cup) whoPicks(pickup pickupSlot) *Player {
	if currentCup.Status != CupStatusPickup {
		return nil
	}
	if pickup.Player < 0 || pickup.Player >= TeamSize {
		return nil
	}
	if pickup.Team < 0 || pickup.Team >= len(currentCup.Teams) {
		return nil
	}
	if pickup.Player == 0 {
		return &currentCup.Manager
	}
	index := currentCup.Teams[pickup.Team].First
	if index < 0 || index >= len(currentCup.Players) {
		return nil
	}
	return &currentCup.Players[index]
}

func (currentCup *Cup) updateTeamNameCache() {
	currentCup.longestTeamName = 0
	currentCup.longestTeamDescription = 0

	for i := 0; i < len(currentCup.Teams); i++ {
		length := len(currentCup.Teams[i].Name)
		if length > currentCup.longestTeamName {
			currentCup.longestTeamName = length
		}

		length += digits10(i) + 2 // number, dot, space
		if length > currentCup.longestTeamDescription {
			currentCup.longestTeamDescription = length
		}
	}
}

func (currentCup *Cup) chooseTeamNames() {
	// Re-seed RNG
	rand.Seed(time.Now().UTC().UnixNano())

	for i := 0; i < len(currentCup.Teams); i++ {
		currentTeam := &currentCup.Teams[i]

		for retry := 0; retry < 100; retry++ {
			currentTeam.nameIndex = rand.Intn(TeamNameCombos)
			attrib, noun := decomposeName(currentTeam.nameIndex)
			found := false
			for j := 0; j < i; j++ {
				otherTeam := &currentCup.Teams[j]
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
		attrib, noun := decomposeName(currentTeam.nameIndex)
		currentTeam.Name = Attributes[attrib] + " " + Nouns[noun]
	}

	currentCup.updateTeamNameCache()
}

// Returns formatted join message or an error
func (currentCup *Cup) addPlayerToTeam(playerIndex int, teamIndex int) (string, error) {
	if playerIndex < 0 || playerIndex >= len(currentCup.Players) {
		return "", fmt.Errorf("player index out of range: %d", playerIndex)
	}
	if teamIndex < 0 || teamIndex >= len(currentCup.Teams) {
		return "", fmt.Errorf("team index out of range: %d", teamIndex)
	}

	player := &currentCup.Players[playerIndex]
	if player.Team != -1 {
		return "", fmt.Errorf("already assigned to %d", player.Team)
	}

	player.Team = teamIndex
	team := &currentCup.Teams[teamIndex]
	if team.First == -1 {
		team.First = playerIndex
		team.Last = playerIndex
	} else {
		lastPlayer := &currentCup.Players[team.Last]
		lastPlayer.Next = playerIndex
		team.Last = playerIndex
	}

	currentCup.PickedPlayers++

	message := mention(player) + " joined team " + strconv.Itoa(teamIndex+1) + ", " + bold(currentCup.Teams[teamIndex].Name)
	if team.First == playerIndex {
		message += " (as captain)"
	}

	return message + ".\n", nil
}

func (currentCup *Cup) getLineup(index int) (string, error) {
	if index < 0 || index >= len(currentCup.Teams) {
		return "", fmt.Errorf("index out of range: %d", index)
	}
	team := &currentCup.Teams[index]
	lineup := ""
	for playerIndex, count := team.First, 0; playerIndex != -1; count++ {
		player := &currentCup.Players[playerIndex]
		if count != 0 {
			lineup += ", "
		}
		lineup += player.Name
		playerIndex = player.Next
	}
	return lineup, nil
}

func (currentCup *Cup) report(selector int) string {
	message := ""

	playerDigits := digits10(len(currentCup.Players))

	switch currentCup.Status {
	case CupStatusSignup:
		if (selector & CupReportPlayers) != 0 {
			if len(currentCup.Players) == 0 {
				message += "No players signed up for the cup so far.\n"
			} else {
				message += numbered(len(currentCup.Players), "player") + " signed up so far:\n```"
				for i := range currentCup.Players {
					message += rightpad(strconv.Itoa(i+1)+". ", playerDigits+2) + currentCup.Players[i].Name + "\n"
				}
				message += "```\n"
			}
		}
		if (selector & CupReportNextAction) != 0 {
			message += "Sign up now by typing " + bold(commandAdd.syntax()) + "\n"
		}

	case CupStatusPickup:
		active := currentCup.activePlayerCount()
		if (selector & CupReportTeams) != 0 {
			if currentCup.PickedPlayers != active && currentCup.PickedPlayers != 0 {
				message += fmt.Sprintf("%d teams, with %s picked out of %d:\n```\n", len(currentCup.Teams), numbered(currentCup.PickedPlayers, "player"), active)
			} else {
				message += fmt.Sprintf("%d competing teams:\n```\n", len(currentCup.Teams))
			}
			for i := range currentCup.Teams {
				lineup, _ := currentCup.getLineup(i)
				teamDescription := strconv.Itoa(i+1) + ". " + currentCup.Teams[i].Name
				// omit colons if all teams are empty
				if currentCup.PickedPlayers > 0 {
					message += fmt.Sprintf("%*s : %s\n", -currentCup.longestTeamDescription, teamDescription, lineup)
				} else {
					message += teamDescription + "\n"
				}
			}
			message += "```\n"
		}

		if (selector & CupReportPlayers) != 0 {
			unpicked := active - currentCup.PickedPlayers
			if unpicked > 0 {
				message += strconv.Itoa(unpicked) + " available players:\n```\n"
				for i := 0; i < active; i++ {
					player := &currentCup.Players[i]
					if player.Team != -1 {
						continue
					}
					message += rightpad(strconv.Itoa(i+1)+". ", playerDigits+2) + player.Name + "\n"
				}
				message += "\n```\n"
			}
		}

		if (selector & CupReportSubs) != 0 {
			subs := len(currentCup.Players) - active
			if subs > 0 {
				message += numbered(subs, " substitute player") + ":\n```\n"
				for i := active; i < len(currentCup.Players); i++ {
					player := &currentCup.Players[i]
					message += strconv.Itoa(i+1) + ". " + player.Name + "\n"
				}
				message += "\n```\n"
			}
		}

		if (selector & CupReportNextAction) != 0 {
			pickup := currentCup.currentPickup()
			who := currentCup.whoPicks(pickup)

			if who != nil {
				teamName := currentCup.Teams[pickup.Team].Name
				teamDescription := "team " + strconv.Itoa(pickup.Team+1) + ", " + bold(teamName)

				if pickup.Player == 0 {
					message += mention(who) + ", pick a captain for " + teamDescription + ", by typing " + bold(commandPick.syntax()) + "\n"
				} else {
					message += mention(who) + ", pick the " + nth(pickup.Player+1) + " player for " + teamDescription + ", by typing " + bold(commandPick.syntax()) + "\n"
				}
			} else {
				message += "Good luck and have fun!\n"
			}
		}
	}

	return message
}

func (currentCup *Cup) removeLastReply(s *discordgo.Session) {
	if len(currentCup.LastReplyID) > 0 {
		s.ChannelMessageDelete(currentCup.ChannelID, currentCup.LastReplyID)
		currentCup.LastReplyID = ""
	}
}

func (currentCup *Cup) reply(s *discordgo.Session, text string, report int) {
	currentCup.removeLastReply(s)
	if report != 0 {
		text += currentCup.report(report)
	}
	message, err := s.ChannelMessageSend(currentCup.ChannelID, text)
	if err == nil {
		currentCup.LastReplyID = message.ID
	}
}

func (currentCup *Cup) deleteAndReply(s *discordgo.Session, m *discordgo.MessageCreate, text string, report int) {
	currentCup.removeLastReply(s)
	s.ChannelMessageDelete(m.ChannelID, m.ID)
	currentCup.reply(s, text, report)
}

func (currentCup *Cup) unpinAll(s *discordgo.Session) {
	allPinned, err := s.ChannelMessagesPinned(currentCup.ChannelID)
	if err == nil {
		for _, pinnedMessage := range allPinned {
			if pinnedMessage.Author.ID == BotID {
				s.ChannelMessageUnpin(pinnedMessage.ChannelID, pinnedMessage.ID)
			}
		}
	}
}

func lastPinned(s *discordgo.Session, ChannelID string) (*discordgo.Message, error) {
	allPinned, err := s.ChannelMessagesPinned(ChannelID)
	if err != nil {
		return nil, err
	}
	for i := len(allPinned) - 1; i >= 0; i-- {
		pinnedMessage := allPinned[i]
		if pinnedMessage.Author.ID == BotID {
			return pinnedMessage, nil
		}
	}
	return nil, nil
}

func getActiveGuildChannels(s *discordgo.Session, GuildID string) ([]*discordgo.Channel, error) {
	channels, err := s.GuildChannels(GuildID)
	if err != nil {
		return nil, err
	}
	count := 0
	for _, channel := range channels {
		cup := getCup(channel.ID)
		if cup != nil && cup.Status != CupStatusInactive {
			channels[count] = channel
			count++
		}
	}
	return channels[:count], nil
}

func getAlternativeChannels(s *discordgo.Session, ChannelID string) ([]*discordgo.Channel, error) {
	channel, err := s.Channel(ChannelID)
	if err != nil {
		return nil, err
	}
	return getActiveGuildChannels(s, channel.GuildID)
}

func mentionChannelAlternatives(s *discordgo.Session, ChannelID string) (message string, err error) {
	others, err := getAlternativeChannels(s, ChannelID)
	if err != nil {
		return
	}

	for i, channel := range others {
		if i != 0 {
			if i == len(others)-1 {
				message += " or "
			} else {
				message += ", "
			}
		}
		message += "<#" + channel.ID + ">"
	}

	return
}

func noCupHereMessage(s *discordgo.Session, m *discordgo.MessageCreate) string {
	// If there are active cups in other channels, we let the user know.
	alternatives, _ := mentionChannelAlternatives(s, m.ChannelID)
	if len(alternatives) <= 0 {
		return "No cup in progress in this channel. You can start one with " + bold(commandStart.syntax())
	}

	return bold(escape(m.Author.Username)) + ", there's no cup in progress in this channel.\nTry again in " +
		alternatives + ", or start a new cup here with " + bold(commandStart.syntax())
}

func (currentCup *Cup) save() error {
	if len(ChannelDataDir) <= 0 {
		return os.ErrInvalid
	}

	err := os.MkdirAll(ChannelDataDir, 0777)
	if err != nil {
		return err
	}

	contents, err := json.Marshal(currentCup)
	if err != nil {
		return err
	}

	path := filepath.Join(ChannelDataDir, currentCup.ChannelID)
	err = ioutil.WriteFile(path, contents, SaveFilePermission)
	if err != nil {
		return err
	}

	return nil
}

////////////////////////////////////////////////////////////////

func digits10(number int) int {
	count := 1
	for ; number >= 10; number /= 10 {
		count++
	}
	return count
}

func rightpad(text string, total int) string {
	if len(text) >= total {
		return text
	}
	return text + strings.Repeat(" ", total-len(text))
}

func numbered(count int, singular string) string {
	result := strconv.Itoa(count) + " " + singular
	if count != 1 {
		result += "s"
	}
	return result
}

func nth(index int) string {
	if index == 1 {
		return "1st"
	}
	if index == 2 {
		return "2nd"
	}
	if index == 3 {
		return "3rd"
	}
	return fmt.Sprintf("%dth", index)
}

func escape(s string) string {
	s = strings.Replace(s, "_", "\\_", -1)
	s = strings.Replace(s, "*", "\\*", -1)
	s = strings.Replace(s, "`", "\\`", -1)
	return s
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

func mentionUser(UserID string) string {
	return "<@" + UserID + ">"
}

func mentionChannel(ChannelID string) string {
	return "<#" + ChannelID + ">"
}

func mention(who *Player) string {
	return mentionUser(who.ID)
}

func display(who *Player) string {
	return bold(escape(who.Name))
}

////////////////////////////////////////////////////////////////

// Common longer durations
const (
	Day   = 24 * time.Hour
	Week  = 7 * Day
	Month = 30 * Day
	Year  = 365 * Day
)

func humanize(duration time.Duration) string {
	if duration < 0 {
		duration = -duration
	}

	type relevantDuration struct {
		time.Duration
		Name string
	}

	var (
		relevantDurations = [...]relevantDuration{
			{time.Second, "second"},
			{time.Minute, "minute"},
			{time.Hour, "hour"},
			{Day, "day"},
			{Week, "week"},
			{Month, "month"},
			{12 * Month, "year"}, // for a humanized string, this is better than the exact value; e.g. for 345 days ~= 12 months < 1 year!
		}
	)

	n := sort.Search(len(relevantDurations), func(i int) bool {
		rounded := duration
		if i > 0 {
			// round up to a multiple of the previous unit
			// e.g. when considering hours, round up to the next minute
			// this way, 59 minutes and 33 seconds = 1 hour
			rounded += relevantDurations[i-1].Duration / 2
		}
		return relevantDurations[i].Duration > rounded
	}) - 1

	if n < 0 {
		n = 0
	}

	duration += relevantDurations[n].Duration / 2
	nano := duration.Nanoseconds()
	major := nano / relevantDurations[n].Nanoseconds()

	return numbered(int(major), relevantDurations[n].Name)
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

// Update bot status, giving users a starting point.
func updateBotStatus(s *discordgo.Session) error {
	err := s.UpdateStatus(0, "type "+draftCommands.prefix)
	if err != nil {
		fmt.Println("error updating bot status,", err)
	}
	return err
}

// This function will be called every time a new message is created
// on any channel that the autenticated bot has access to.
func onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore all messages created by the bot itself
	if m.Author.ID == BotID {
		return
	}

	for _, group := range commandGroups {
		if len(m.Content) < len(group.prefix) {
			continue
		}

		prefix := strings.ToLower(m.Content[:len(group.prefix)])
		if prefix != group.prefix {
			continue
		}

		command := m.Content[len(group.prefix):]
		command = strings.TrimSpace(command)

		var token string
		token, command = parseToken(command)

		if len(token) == 0 {
			commandHelp.execute("", s, m)
			return
		}

		token = strings.ToLower(token)

		for _, cmd := range group.commands {
			if cmd.name == token {
				cmd.execute(command, s, m)
				return
			}
		}

		_, _ = s.ChannelMessageSend(m.ChannelID, "Unknown command, '"+token+"'.\n")
		commandHelp.execute("", s, m)
		return

	}

	handleChat(s, m)
}

func onReady(s *discordgo.Session, m *discordgo.Ready) {
	updateBotStatus(s)
}

func onResumed(s *discordgo.Session, m *discordgo.Resumed) {
	updateBotStatus(s)
}

// Handle draft cup start command
func handleStart(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup != nil {
		message := bold(escape(m.Author.Username)) + ", "
		if currentCup.Manager.ID == m.Author.ID {
			message += "you"
		} else {
			message += display(&currentCup.Manager)
		}
		message += " already started the cup."
		_, _ = s.ChannelMessageSend(m.ChannelID, message)
		currentCup.reply(s, "", CupReportAll)
		return
	}

	currentCup = addCup(m.ChannelID)
	currentCup.Manager = makePlayer(m.Author)
	currentCup.Description = args

	channel, err := s.Channel(m.ChannelID)
	if err != nil {
		fmt.Println("Could not retrieve channel info:", err.Error())
	} else {
		currentCup.GuildID = channel.GuildID
	}

	text := "Hey, @everyone!\n\nRegistration is now open for a new draft cup, managed by " + bold(escape(m.Author.Username)) + ".\n\n"
	if len(args) > 0 {
		text += args + "\n\n"
	}
	text += "You can sign up now by typing " + bold(commandAdd.syntax())

	currentCup.StartTime = time.Now()
	currentCup.NextPromoteTime = currentCup.StartTime.Add(MinimumPromotionInterval)
	currentCup.NextPromoteTimeManager = currentCup.StartTime.Add(MinimumPromotionIntervalManager)

	s.ChannelMessageDelete(m.ChannelID, m.ID)
	message, err := s.ChannelMessageSend(currentCup.ChannelID, text)
	if err != nil {
		fmt.Println("Unable to send cup start message, aborting cup: ", err)
		deleteCup(currentCup.ChannelID)
	} else {
		currentCup.unpinAll(s)
		currentCup.StartMessageID = message.ID
		s.ChannelMessagePin(currentCup.ChannelID, message.ID)
	}
}

// Handle draft cup abort command
func handleAbort(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Can't abort a cup that hasn't started.")
		return
	}

	if !currentCup.isSuperUser(m.Author.ID) {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Only "+display(&currentCup.Manager)+", the cup manager, or an admin can abort this cup.")
		return
	}

	_, _ = s.ChannelMessageSend(m.ChannelID, "Cup aborted by "+bold(escape(m.Author.Username))+". You can start a new one with "+bold(commandStart.syntax()))
	currentCup.unpinAll(s)
	deleteCup(m.ChannelID)
}

// Handle draft cup sign up
func handleAdd(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil || currentCup.Status == CupStatusInactive {
		_, _ = s.ChannelMessageSend(m.ChannelID, noCupHereMessage(s, m))
		return
	}

	switch currentCup.Status {
	case CupStatusSignup, CupStatusPickup:
		before := currentCup.findPlayer(m.Author.ID)
		if before != -1 && !devHacks.allowDuplicates {
			message := bold(escape(m.Author.Username)) + ", you're already registered for this cup (" + nth(before+1) + " of " + strconv.Itoa(len(currentCup.Players)) + ")."
			_, _ = s.ChannelMessageSend(m.ChannelID, message)
			currentCup.reply(s, "", CupReportAll)
		} else {
			currentCup.Players = append(currentCup.Players, makePlayer(m.Author))
			if currentCup.Status != CupStatusSignup {
				message := mentionUser(m.Author.ID) + " joined the cup as " + nth(len(currentCup.Players)-currentCup.activePlayerCount()) + " substitute."
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
			}
			currentCup.deleteAndReply(s, m, "", CupReportAll)
		}

	default:
		message := "Sorry, " + bold(escape(m.Author.Username)) + ", cup is no longer open for signup."
		_, _ = s.ChannelMessageSend(m.ChannelID, message)
		currentCup.reply(s, "", CupReportAll)
	}
}

// Handle draft cup withdrawals
func handleRemove(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil {
		_, _ = s.ChannelMessageSend(m.ChannelID, "No cup in progress in this channel, anyway.")
		return
	}

	switch currentCup.Status {
	case CupStatusSignup, CupStatusPickup:
		if len(currentCup.Players) == 0 {
			_, _ = s.ChannelMessageSend(m.ChannelID, "No players to remove, nobody has signed up for the cup yet.")
			return
		}

		var which int
		var token string
		token, args = parseToken(args)
		if len(token) > 0 {
			if !currentCup.isManager(m.Author.ID) {
				message := "Only the cup manager, " + display(&currentCup.Manager) + ", can remove other players.\n"
				if currentCup.findPlayer(m.Author.ID) != -1 {
					message += "You can remove yourself by typing " + bold(commandRemove.syntaxNoArgs())
				}
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
				currentCup.reply(s, "", CupReportAll)
				return
			}

			index, err := strconv.Atoi(token)
			if err != nil {
				message := bold(escape(m.Author.Username)) + ", '" + token + "' doesn't look like a number, either leave it out (to remove yourself from the list of players) or specify an actual player number.\n\n"
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
				currentCup.reply(s, "", CupReportAll)
				return
			}
			index-- // 0-based

			if index < 0 || index >= len(currentCup.Players) {
				message := bold(escape(m.Author.Username)) + ", " + token + " is not a valid player number."
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
				currentCup.reply(s, "", CupReportAll)
				return
			}

			which = index
		} else {
			which = currentCup.findPlayer(m.Author.ID)
			if which == -1 {
				_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+", you're not registered for this cup anyway.")
				currentCup.reply(s, "", CupReportAll)
				return
			}
		}

		if currentCup.Status >= CupStatusPickup {
			active := currentCup.activePlayerCount()
			player := &currentCup.Players[which]

			// if the player to be removed isn't a substitute
			if which < active {
				// ...but a substitute is available
				if active < len(currentCup.Players) {
					sub := &currentCup.Players[active]
					sub.ID, player.ID = player.ID, sub.ID
					sub.Name, player.Name = player.Name, sub.Name
					which = active
					message := mention(player) + " has left the cup and " + mention(sub) + " will take his place."
					s.ChannelMessageSend(m.ChannelID, message)
				} else {
					var target string
					if m.Author.ID == player.ID {
						target = "you"
					} else {
						target = mention(player)
					}

					message := bold(escape(m.Author.Username)) + ", there's no substitute available to replace " + target +
						".\nYou need to find a substitute first and have him sign up by typing " + bold(commandAdd.syntax())
					s.ChannelMessageSend(m.ChannelID, message)
					return
				}
			} else {
				message := mention(player) + " has left the cup."
				s.ChannelMessageSend(m.ChannelID, message)
			}
		}

		currentCup.Players = append(currentCup.Players[:which], currentCup.Players[which+1:]...)
		currentCup.deleteAndReply(s, m, "", CupReportAll)

	default:
		_, _ = s.ChannelMessageSend(m.ChannelID, "Cup is not currently open for signup, anyway.")
	}
}

// Handle draft cup registration close
func handleClose(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil {
		_, _ = s.ChannelMessageSend(m.ChannelID, "No cup in progress in this channel, no sign-ups to close.")
		return
	}

	if !currentCup.isManager(m.Author.ID) {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Only "+display(&currentCup.Manager)+", the cup manager, can close sign-up.")
		return
	}

	s.ChannelMessageDelete(m.ChannelID, m.ID)

	switch currentCup.Status {
	case CupStatusSignup:
		// Hack to allow testing
		if devHacks.fillUpOnClose > 0 {
			if len(currentCup.Players) == 0 {
				currentCup.Players = append(currentCup.Players, currentCup.Manager)
			}
			for i := len(currentCup.Players); i < devHacks.fillUpOnClose; i++ {
				currentCup.Players = append(currentCup.Players, currentCup.Players[0])
			}
		}

		signedUp := len(currentCup.Players)
		if signedUp < MinimumPlayers {
			var who string
			if signedUp == 0 {
				who = "Nobody"
			} else {
				who = "Only " + numbered(signedUp, "player")
			}
			_, _ = s.ChannelMessageSend(currentCup.ChannelID, who+" signed up, cup aborted.")
			currentCup.unpinAll(s)
			deleteCup(m.ChannelID)
			return
		}

		var token string
		token, args = parseToken(args)
		if len(token) != 0 {
			count, err := strconv.Atoi(token)
			if err != nil {
				message := bold(escape(m.Author.Username)) + ", '" + token + "' doesn't look like a number, either leave it out or specify an actual number of players to keep.\n"
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
				currentCup.reply(s, "", CupReportAll)
				return
			}
			if count > signedUp {
				message := bold(escape(m.Author.Username)) + ", " + token + " players haven't signed up yet.\n"
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
				currentCup.reply(s, "", CupReportAll)
				return
			}
			if count < MinimumPlayers {
				message := bold(escape(m.Author.Username)) + ", you need to keep at least " + strconv.Itoa(MinimumPlayers) + " players.\n"
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
				currentCup.reply(s, "", CupReportAll)
				return
			}
			signedUp = count
		}

		numTeams := signedUp / TeamSize

		currentCup.Status = CupStatusPickup
		currentCup.Teams = make([]Team, numTeams)
		for i := 0; i < numTeams; i++ {
			currentTeam := &currentCup.Teams[i]
			currentTeam.resetTeam()
		}
		currentCup.chooseTeamNames()

		message := fmt.Sprintf("Cup registration is now closed.\n\n")
		currentCup.reply(s, message, CupReportAll)

	default:
		_, _ = s.ChannelMessageSend(m.ChannelID, "Too late, "+bold(escape(m.Author.Username))+", registration for this cup is already closed.")
	}
}

// Handle draft cup player picking
func handlePick(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil {
		_, _ = s.ChannelMessageSend(m.ChannelID, "No cup in progress in this channel. You can start one with "+bold(commandStart.syntax()))
		return
	}

	switch currentCup.Status {
	case CupStatusSignup:
		message := bold(escape(m.Author.Username)) + ", we're not picking players yet.\n"
		_, _ = s.ChannelMessageSend(m.ChannelID, message)
		currentCup.reply(s, "", CupReportAll)
		return

	case CupStatusPickup:
		pickup := currentCup.currentPickup()
		who := currentCup.whoPicks(pickup)
		numActive := currentCup.activePlayerCount()

		if who == nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+", it's not your turn to pick.\n")
			currentCup.reply(s, "", CupReportAll^CupReportSubs)
			return
		}

		if who.ID != m.Author.ID {
			_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+", it's not your turn to pick, but "+display(who)+"'s.\n")
			currentCup.reply(s, "", CupReportAll^CupReportSubs)
			return
		}

		var token string
		token, args = parseToken(args)
		if len(token) == 0 {
			_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+", you need to specify a player number.")
			currentCup.reply(s, "", CupReportAll^CupReportSubs)
			return
		}
		index, err := strconv.Atoi(token)
		if err != nil {
			_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+", '"+token+"' doesn't look like a number. You need to specify a player number.")
			currentCup.reply(s, "", CupReportAll^CupReportSubs)
			return
		}
		index-- // 0-based

		if index < 0 || index >= len(currentCup.Players) {
			_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+", '"+token+"' is not a valid player number.")
			currentCup.reply(s, "", CupReportAll^CupReportSubs)
			return
		}

		if index >= numActive && index < len(currentCup.Players) {
			sub := &currentCup.Players[index]
			message := bold(escape(m.Author.Username)) + ", you can't pick " + display(sub) + ", he's only registered as a substitute."
			_, _ = s.ChannelMessageSend(m.ChannelID, message)
			currentCup.reply(s, "", CupReportAll^CupReportSubs)
			return
		}

		selected := &currentCup.Players[index]
		if selected.Team != -1 {
			team := currentCup.Teams[selected.Team]
			message := display(selected) + " already on team " + strconv.Itoa(selected.Team+1) + ", " + bold(team.Name)
			_, _ = s.ChannelMessageSend(m.ChannelID, message)
			currentCup.reply(s, "", CupReportAll^CupReportSubs)
			return
		}

		text, _ := currentCup.addPlayerToTeam(index, pickup.Team)

		// The last player isn't picked, but automatically assigned to the remaining slot.
		if currentCup.PickedPlayers == numActive-1 {
			currentCup.removeLastReply(s)
			s.ChannelMessageDelete(m.ChannelID, m.ID)

			lastPlayer := currentCup.nextAvailablePlayer()
			lastSlot := currentCup.currentPickup()
			lastJoin, _ := currentCup.addPlayerToTeam(lastPlayer, lastSlot.Team)
			text += lastJoin

			// We send the last two join messages separately, instead of merging them with the final report.
			// This way, the last two players to get picked aren't highlighted at the end if the report mentions @everyone.
			_, _ = s.ChannelMessageSend(currentCup.ChannelID, text)

			currentCup.unpinAll(s)

			text = "Teams are now complete and the games can begin!\n" +
				display(&currentCup.Manager) + " will take things from here, setting up matches and tracking scores.\n\n" +
				currentCup.report(CupReportTeams|CupReportSubs) +
				"Good luck and have fun, @everyone!"

			lastMessage, err := s.ChannelMessageSend(currentCup.ChannelID, text)
			if err == nil {
				s.ChannelMessagePin(lastMessage.ChannelID, lastMessage.ID)
			}

			deleteCup(currentCup.ChannelID)
			return
		}

		currentCup.removeLastReply(s)
		s.ChannelMessageDelete(m.ChannelID, m.ID)
		_, _ = s.ChannelMessageSend(currentCup.ChannelID, text)
		currentCup.reply(s, "", CupReportAll^CupReportSubs)

	default:
		_, _ = s.ChannelMessageSend(m.ChannelID, "Sorry, "+bold(escape(m.Author.Username))+", we're not picking players at this point.")
		currentCup.reply(s, "", CupReportAll)
		return
	}
}

// Handle draft cup promotion
func handlePromote(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil || currentCup.Status == CupStatusInactive {
		_, _ = s.ChannelMessageSend(m.ChannelID, noCupHereMessage(s, m))
		return
	}

	if currentCup.Status != CupStatusSignup {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Cup can only be promoted when registration is open.")
		return
	}

	s.ChannelMessageDelete(m.ChannelID, m.ID)

	var nextTime *time.Time
	if currentCup.isSuperUser(m.Author.ID) {
		nextTime = &currentCup.NextPromoteTimeManager
	} else {
		nextTime = &currentCup.NextPromoteTime
	}

	now := time.Now()
	remaining := nextTime.Sub(now)
	if remaining > 0 {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Too soon to promote, "+bold(escape(m.Author.Username))+". You can try again in "+humanize(remaining)+".")
		return
	}

	currentCup.NextPromoteTime = now.Add(MinimumPromotionInterval)
	currentCup.NextPromoteTimeManager = now.Add(MinimumPromotionIntervalManager)

	text := "Hey, @everyone!\n\nDon't forget that registration is now open for a new draft cup, managed by " + display(&currentCup.Manager) + ".\n"
	if len(currentCup.Description) > 0 {
		text += "\n" + currentCup.Description
	}
	_, _ = s.ChannelMessageSend(m.ChannelID, text)
	currentCup.reply(s, "", CupReportAll)
}

// Handle draft cup player list info command
func handleWho(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil || currentCup.Status == CupStatusInactive {
		message := noCupHereMessage(s, m)
		pinned, _ := lastPinned(s, m.ChannelID)
		if pinned != nil {
			// Apparently, ContentWithMentionsReplaced *doesn't* replace @everyone...
			previous := strings.Replace(pinned.ContentWithMentionsReplaced(), "@everyone", "everyone", -1)

			message += "\n\n__***Last pinned cup message"
			when, err := pinned.Timestamp.Parse()
			if err == nil {
				delta := time.Now().Sub(when)
				// Only mention elapsed time if it's in the past...
				if delta > 0 {
					message += " (from " + humanize(delta) + " ago)"
				}
			}
			message += ":***__\n\n" + previous
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, message)
		return
	}
	currentCup.deleteAndReply(s, m, "", CupReportAll)

	if devHacks.saveOnWho {
		currentCup.save()
	}
}

// Handle draft cup moderation toggle command
func handleModerate(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil || currentCup.Status == CupStatusInactive {
		_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+", moderation can only be enabled when a cup is active.\n")
		return
	}

	if !currentCup.isSuperUser(m.Author.ID) {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Only "+display(&currentCup.Manager)+", the cup manager, or an admin can enable or disable moderation.")
		currentCup.reply(s, "", CupReportAll^CupReportSubs)
		return
	}

	moderation := !currentCup.Moderated

	var token string
	token, args = parseToken(args)
	token = strings.ToLower(token)

	if len(token) > 0 {
		if token == "on" {
			moderation = true
		} else if token == "off" {
			moderation = false
		} else {
			message := bold(escape(m.Author.Username)) + ", '" + token + "' is not a valid option. You need to specify either **on** or **off** after " + bold(commandModerate.syntaxNoArgs())
			_, _ = s.ChannelMessageSend(m.ChannelID, message)
			currentCup.reply(s, "", CupReportAll^CupReportSubs)
			return
		}
	}

	if moderation == currentCup.Moderated {
		if currentCup.Moderated {
			_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+", this channel is already moderated.")
		} else {
			_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+", this channel is already unmoderated.")
		}
		currentCup.reply(s, "", CupReportAll^CupReportSubs)
		return
	}

	currentCup.Moderated = moderation
	if currentCup.Moderated {
		s.ChannelMessageDelete(m.ChannelID, m.ID)
		_, _ = s.ChannelMessageSend(currentCup.ChannelID, "This channel is now moderated while the cup is active.\nAny message that is not a bot command will be removed.")
	} else {
		s.ChannelMessageDelete(m.ChannelID, m.ID)
		_, _ = s.ChannelMessageSend(currentCup.ChannelID, "This channel is no longer moderated.")
	}
}

// Handle draft reopen command
func handleReopen(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil || currentCup.Status == CupStatusInactive {
		_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+", there's no cup in progress in this channel.\n")
		return
	}

	s.ChannelMessageDelete(m.ChannelID, m.ID)

	if currentCup.Status != CupStatusPickup {
		_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+", the cup can be only reopen for sign-up after picking has begun.")
		currentCup.reply(s, "", CupReportAll^CupReportSubs)
		return
	}

	if !currentCup.isManager(m.Author.ID) {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Only "+display(&currentCup.Manager)+", the cup manager, can discard current teams and reopen the cup for sign-up.")
		currentCup.reply(s, "", CupReportAll^CupReportSubs)
		return
	}

	currentCup.Teams = nil
	for i := range currentCup.Players {
		player := &currentCup.Players[i]
		player.resetTeam()
	}
	currentCup.Status = CupStatusSignup

	_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+" discarded the teams and reopened the cup.")
	currentCup.reply(s, "", CupReportAll)
}

// Handle draft cup help command
func handleHelp(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	message := "Supported commands:\n```Note: arguments marked [] are optional, <> are mandatory.\n\n"

	for i, group := range commandGroups {
		if i > 0 {
			message += "\n"
		}

		if len(commandGroups) > 0 {
			message += group.description + ":\n"
		}

		maxSyntaxLength := 0
		for _, cmd := range group.commands {
			length := cmd.syntaxLength()
			if length > maxSyntaxLength {
				maxSyntaxLength = length
			}
		}

		for _, cmd := range group.commands {
			message += fmt.Sprintf("%*s : %s\n", -maxSyntaxLength, cmd.syntax(), cmd.help)
		}
	}

	message += "```\n"

	_, _ = s.ChannelMessageSend(m.ChannelID, message)
}

// Handle chat messages that don't belong to any command group
func handleChat(s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil || currentCup.Status == CupStatusInactive || !currentCup.Moderated {
		return
	}
	s.ChannelMessageDelete(m.ChannelID, m.ID)
}

////////////////////////////////////////////////////////////////
// Random team name support
////////////////////////////////////////////////////////////////

func decomposeName(index int) (int, int) {
	attribute := index % len(Attributes)
	noun := index / len(Attributes)
	return attribute, noun
}

// Random team names
var (
	Attributes = [...]string{
		"Black", "Grey", "Purple", "Brown", "Blue", "Red", "Green", "Magenta",
		"Silent", "Quiet", "Loud", "Thundering", "Screaming", "Flaming", "Furious", "Zen", "Chill",
		"Jolly", "Giggly", "Unimpressed", "Serious",
		"Inappropriate", "Indecent", "Sexy", "Hot", "Flirty", "Cheeky", "Cheesy", "Shameless", "Provocative", "Offensive", "Defensive",
		"Gangster", "Fugitive", "Outlaw", "Pirate", "Thug", "Kleptomaniac", "Killer", "Lethal", "Gunslinging",
		"Fresh", "Rookie", "Trained", "Major", "Grandmaster", "Retired", "Potent", "Mighty", "Convincing", "Commanding", "Punchy",
		"Lucky", "Tryhard", "Stronk",
		"Expendable",
		"Millenial", "Centennial",
		"Lunar", "Solar", "Martian",
		"Aerodynamic",
		"Sprinting", "Strafing", "Strafejumping", "Circlejumping", "Bunnyhopping", "Crouching", "Rising", "Standing", "Camping", "Twitchy", "Sniping", "Telefragging",
		"Rolling", "Dancing", "Breakdancing", "Tapdancing", "Clubbing",
		"Snorkeling", "Snowbording", "Cycling", "Rollerblading", "Paragliding", "Skydiving",
		"Drifting", "Warping", "Laggy", "Smooth", "Stiff",
		"Cryogenic", "Mutating", "Undead", "Ghostly", "Possessed", "Supernatural",
		"Juggling", "Ambidextrous", "Left-handed",
		"Snoring", "Sleepy", "Energetic", "Hyperactive", "Dynamic",
		"Tilted", "Excentric", "Irrational", "Claustrophobic",
		"Undercover", "Stealthy", "Hidden", "Obvious", "Deceptive",
		"Total", "Definitive",
		"Chocolate", "Vanilla",
		"Plastic", "Metal", "Rubber", "Golden", "Silver", "Paper",
		"Random", "Synchronized", "Synergetic", "Coordinated",
		"Radical", "Unconventional", "Standard", "Original", "Mutated", "Creative", "Articulate", "Elegant", "Gentle", "Polite", "Classy",
		"Retro", "Old-school", "Next-gen", "Revolutionary",
		"Punk", "Disco", "Electronic", "Analog", "Mechanical",
		"Wireless", "Aircooled", "Watercooled", "Overvolted", "Overclocked", "Idle", "Hyperthreaded", "Freesync", "G-Sync", "Crossfire", "SLI", "Quad-channel",
		"Optimized", "Registered", "Licensed",
		"Nerdy", "Hipster", "Trendy", "Sporty", "Chic", "Photogenic",
		"Mythical", "Famous", "Incognito",
		"Slim", "Toned", "Muscular", "Round", "Heavy", "Well-fed", "Hungry", "Vegan",
		"Bearded", "Hairy", "Furry", "Fuzzy",
		"Beastly", "Barbarian", "Vicious", "Fierce", "Devastating", "Dominating", "Conquering", "Controlling", "Agressive", "Retaliating",
		"Fearless", "Heroic", "Glorious", "Victorious", "Triumphant", "Relentless", "Unstoppable", "Spectacular", "Impressive", "Rampage",
		"Arctic", "Polar", "Siberian", "Tropical", "Brazilian",
	}

	Nouns = [...]string{
		"Alligators", "Crocs",
		"Armadillos", "Beavers", "Squirrels", "Raccoons",
		"Bears", "Pandas",
		"Hamsters", "Kittens", "Bunnies", "Puppies", "Pitbulls", "Bulldogs", "Dalmatians", "Greyhounds", "Huskies",
		"Turtles",
		"Giraffes", "Gazelles",
		"Sharks", "Piranhas", "Tuna", "Salmons", "Trouts", "Barracudas", "Stingrays",
		"Dolphins", "Sealions",
		"Hornets",
		"Pythons", "Vipers", "Cobras", "Anacondas",
		"Hippos", "Rhinos",
		"Tigers", "Cheetas", "Hyenas", "Dingos",
		"Baboons", "Bonobos",
		"Dragons", "Pterodactyls", "Eagles", "Hawks", "Ravens", "Seagulls", "Flamingos", "Pigeons", "Roosters", "Duckies",
		"Ponies", "Zebras", "Stallions",
		"Zombies", "Unicorns", "Mermaids", "Trolls",
	}

	TeamNameCombos = len(Attributes) * len(Nouns)
)

////////////////////////////////////////////////////////////////

// Discord session
var (
	Session *discordgo.Session
)

// Variables used for command line parameters
var (
	Token string
	BotID string

	// Developer hacks, for easier testing
	devHacks struct {
		fillUpOnClose   int
		allowDuplicates bool
		saveOnWho       bool
	}
)

////////////////////////////////////////////////////////////////

// Permission to be used when saving files
const (
	SaveFilePermission = os.FileMode(0640)
)

// Folder where channel-specific (cup) files are saved
var (
	ChannelDataDir string
)

// Load all cups from disk (and remove the corresponding files)
func resumeState() error {
	if len(ChannelDataDir) <= 0 {
		return os.ErrNotExist
	}

	fileList, err := ioutil.ReadDir(ChannelDataDir)
	if err != nil {
		return err
	}

	for _, file := range fileList {
		if file.IsDir() {
			continue
		}
		name := file.Name()
		path := filepath.Join(ChannelDataDir, name)
		contents, err := ioutil.ReadFile(path)
		if err != nil {
			fmt.Println("Error reading cup", name, ":", err)
			continue
		}

		currentCup := new(Cup)
		err = json.Unmarshal(contents, currentCup)
		if err != nil {
			fmt.Println("Error parsing cup", name, ":", err)
			continue
		}

		if currentCup.ChannelID != name {
			fmt.Printf("File name/channel ID mismatch: '%s' vs '%s', ignoring...\n", name, currentCup.ChannelID)
			continue
		}

		currentCup.updateTeamNameCache()
		activeCups[currentCup.ChannelID] = currentCup

		os.Remove(path)
		fmt.Println("Loaded cup", name)
	}

	return nil
}

// Save all active cups to disk
func suspendState() error {
	for index, cup := range activeCups {
		err := cup.save()
		if err != nil {
			fmt.Println("Error serializing cup", index, ":", err)
			continue
		}
		fmt.Println("Saved cup", index)
	}

	return nil
}

// Application initialization
func init() {
	flag.StringVar(&Token, "t", "", "Bot Token")
	flag.BoolVar(&devHacks.allowDuplicates, "dev-allowdup", false, "Allow multiple sign up")
	flag.BoolVar(&devHacks.saveOnWho, "dev-saveonwho", false, "Save cup on who command")
	flag.IntVar(&devHacks.fillUpOnClose, "dev-autofill", 0, "Number of slots to fill up on close")
	flag.Parse()

	rand.Seed(time.Now().UTC().UnixNano())

	// Commands are initialized here to avoid initialization loop.

	commandHelp = command{
		group:   &draftCommands,
		name:    "help",
		args:    "",
		execute: handleHelp,
		help:    "Show this list",
	}
	commandStart = command{
		group:   &draftCommands,
		name:    "start",
		args:    " [message]",
		execute: handleStart,
		help:    "Start a new cup, with an optional description",
	}
	commandAbort = command{
		group:   &draftCommands,
		name:    "abort",
		args:    "",
		execute: handleAbort,
		help:    "Abort current cup",
	}
	commandAdd = command{
		group:   &draftCommands,
		name:    "add",
		args:    "",
		execute: handleAdd,
		help:    "Sign up to play in the cup",
	}
	commandRemove = command{
		group:   &draftCommands,
		name:    "remove",
		args:    " [number]",
		execute: handleRemove,
		help:    "Remove yourself from the cup (or another player, if admin)",
	}
	commandWho = command{
		group:   &draftCommands,
		name:    "who",
		args:    "",
		execute: handleWho,
		help:    "Show list of players in cup",
	}
	commandModerate = command{
		group:   &draftCommands,
		name:    "moderate",
		args:    " [on|off]",
		execute: handleModerate,
		help:    "Enable/disable or toggle channel moderation when a cup is active",
	}
	commandClose = command{
		group:   &draftCommands,
		name:    "close",
		args:    " [number]",
		execute: handleClose,
		help:    "Close cup for sign-ups, optionally keeping only [number] players",
	}
	commandPick = command{
		group:   &draftCommands,
		name:    "pick",
		args:    " <number>",
		execute: handlePick,
		help:    "Pick the player with the given number",
	}
	commandPromote = command{
		group:   &draftCommands,
		name:    "promote",
		args:    "",
		execute: handlePromote,
		help:    "Promote the cup",
	}
	commandReopen = command{
		group:   &draftCommands,
		name:    "reopen",
		args:    "",
		execute: handleReopen,
		help:    "Discard current teams and reopen cup for sign-up",
	}

	exe, err := os.Executable()
	if err == nil {
		ChannelDataDir = filepath.Join(filepath.Dir(exe), "channels")
		fmt.Println("Data folder: ", ChannelDataDir)

		resumeState()
	}
}

// Application main function
func main() {
	// Create a new Discord session using the provided bot token.
	var err error
	Session, err = discordgo.New("Bot " + Token)
	if err != nil {
		fmt.Println("error creating Discord session,", err)
		return
	}

	// Get the account information.
	u, err := Session.User("@me")
	if err != nil {
		fmt.Println("error obtaining account details,", err)
		return
	}

	// Store the account ID for later use.
	BotID = u.ID

	// Register event callbacks.
	Session.AddHandler(onMessageCreate)
	Session.AddHandler(onReady)
	Session.AddHandler(onResumed)

	// Open the websocket and begin listening.
	err = Session.Open()
	if err != nil {
		fmt.Println("error opening connection,", err)
		return
	}
	defer Session.Close()

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

	suspendState()

	return
}
