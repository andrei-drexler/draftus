package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Cup status
const (
	CupStatusInactive = iota
	CupStatusSignup   = iota
	CupStatusPickup   = iota
)

// Player counts
const (
	DefaultTeamSize = 4
	MinimumTeams    = 2
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
		TeamSize               int

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

func mention(who *Player) string {
	return mentionUser(who.ID)
}

func display(who *Player) string {
	return bold(escape(who.Name))
}

////////////////////////////////////////////////////////////////

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
	currentCup.TeamSize = DefaultTeamSize

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
	target += currentCup.TeamSize - 1
	target -= target % currentCup.TeamSize
	minPlayers := currentCup.minPlayerCount()
	if target < minPlayers {
		target = minPlayers
	}
	return target
}

func (currentCup *Cup) activePlayerCount() int {
	return len(currentCup.Teams) * currentCup.TeamSize
}

func (currentCup *Cup) minPlayerCount() int {
	return currentCup.TeamSize * MinimumTeams
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
	if pickup.Player < 0 || pickup.Player >= currentCup.TeamSize {
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
		message += mentionChannel(channel.ID)
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

////////////////////////////////////////////////////////////////

// Permission to be used when saving files
const (
	SaveFilePermission = os.FileMode(0640)
)

func defaultChannelDataDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "channels")
}

// Folder where channel-specific (cup) files are saved
var (
	ChannelDataDir = defaultChannelDataDir()
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

		if currentCup.TeamSize == 0 {
			currentCup.TeamSize = DefaultTeamSize
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
