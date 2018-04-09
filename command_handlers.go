package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

////////////////////////////////////////////////////////////////

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
		minPlayers := currentCup.minPlayerCount()
		if signedUp < minPlayers {
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
			if count < minPlayers {
				message := bold(escape(m.Author.Username)) + ", you need to keep at least " + strconv.Itoa(minPlayers) + " players.\n"
				_, _ = s.ChannelMessageSend(m.ChannelID, message)
				currentCup.reply(s, "", CupReportAll)
				return
			}
			signedUp = count
		}

		numTeams := signedUp / currentCup.TeamSize

		currentCup.Status = CupStatusPickup
		currentCup.PickedPlayers = 0
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
	currentCup.PickedPlayers = 0

	_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+" discarded the teams and reopened the cup.")
	currentCup.reply(s, "", CupReportAll)
}

// Handle draft cup teamsize command
func handleTeamSize(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil || currentCup.Status == CupStatusInactive {
		_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+", there's no cup in progress in this channel.\n")
		return
	}

	s.ChannelMessageDelete(m.ChannelID, m.ID)

	var token string
	token, args = parseToken(args)
	if len(token) <= 0 {
		message := bold(escape(m.Author.Username)) + ", team size is " + bold(strconv.Itoa(currentCup.TeamSize)) + ".\n"
		_, _ = s.ChannelMessageSend(m.ChannelID, message)
		currentCup.reply(s, "", CupReportAll^CupReportSubs)
		return
	}

	if !currentCup.isManager(m.Author.ID) {
		_, _ = s.ChannelMessageSend(m.ChannelID, "Only "+display(&currentCup.Manager)+", the cup manager, can change team size.")
		currentCup.reply(s, "", CupReportAll^CupReportSubs)
		return
	}

	if currentCup.Status != CupStatusSignup {
		_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+", you can only change team size during sign-up.")
		currentCup.reply(s, "", CupReportAll^CupReportSubs)
		return
	}

	newSize, err := strconv.Atoi(token)
	if err != nil {
		message := bold(escape(m.Author.Username)) + ", '" + token + "' doesn't look like a number.\n\n"
		_, _ = s.ChannelMessageSend(m.ChannelID, message)
		currentCup.reply(s, "", CupReportAll^CupReportSubs)
		return
	}

	if newSize <= 0 {
		message := bold(escape(m.Author.Username)) + ", " + token + " is not a valid team size."
		_, _ = s.ChannelMessageSend(m.ChannelID, message)
		currentCup.reply(s, "", CupReportAll^CupReportSubs)
		return
	}

	if newSize == currentCup.TeamSize {
		message := bold(escape(m.Author.Username)) + ", team size is already " + token + "."
		_, _ = s.ChannelMessageSend(m.ChannelID, message)
		currentCup.reply(s, "", CupReportAll^CupReportSubs)
		return
	}

	currentCup.TeamSize = newSize

	_, _ = s.ChannelMessageSend(m.ChannelID, bold(escape(m.Author.Username))+" has changed team size to "+bold(token)+".")
	currentCup.reply(s, "", CupReportAll^CupReportSubs)
}

// Handle draft cup help command
func handleHelp(args string, s *discordgo.Session, m *discordgo.MessageCreate) {
	message := "Supported commands:\n```Note: arguments marked [] are optional, <> are mandatory.\n\n"

	for i, group := range commandGroups {
		if i > 0 {
			message += "\n"
		}

		if len(commandGroups) > 1 {
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
