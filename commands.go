package main

import (
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
	commandTeamSize command
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
			&commandTeamSize,
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

// Handle chat messages that don't belong to any command group
func handleChat(s *discordgo.Session, m *discordgo.MessageCreate) {
	currentCup := getCup(m.ChannelID)
	if currentCup == nil || currentCup.Status == CupStatusInactive || !currentCup.Moderated {
		return
	}
	s.ChannelMessageDelete(m.ChannelID, m.ID)
}

////////////////////////////////////////////////////////////////

func setupDraftCommands() {
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
	commandTeamSize = command{
		group:   &draftCommands,
		name:    "teamsize",
		args:    " [number]",
		execute: handleTeamSize,
		help:    "Show or change current team size",
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
}

func setupCommands() {
	setupDraftCommands()
}
