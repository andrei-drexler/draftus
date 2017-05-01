package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

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

// Application initialization
func init() {
	flag.StringVar(&Token, "t", "", "Bot Token")
	flag.BoolVar(&devHacks.allowDuplicates, "dev-allowdup", false, "Allow multiple sign up")
	flag.BoolVar(&devHacks.saveOnWho, "dev-saveonwho", false, "Save cup on who command")
	flag.IntVar(&devHacks.fillUpOnClose, "dev-autofill", 0, "Number of slots to fill up on close")
	flag.Parse()

	rand.Seed(time.Now().UTC().UnixNano())

	// Commands are initialized here to avoid an initialization loop.
	setupCommands()

	if len(ChannelDataDir) > 0 {
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
