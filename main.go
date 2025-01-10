package main

import (
	"flag"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"log"
	"net"
	"os"
	"os/signal"
	"time"
)

var (
	RemoveCommands = flag.Bool("rmcmd", false, "Remove all commands after shutdown or not")
	GuildID        = flag.String("guild", "", "Test guild ID. If not passed - bot registers commands globally")
	BF4API         = os.Getenv("BF4DB_API")
	BotToken       = os.Getenv("DISCORD_BOT_TOKEN")
)

var s *discordgo.Session

func init() { flag.Parse() }

func init() {
	var err error
	s, err = discordgo.New("Bot " + BotToken)
	if err != nil {
		log.Fatalf("Invalid bot parameters: %v", err)
	}
}

var (
	integerOptionMinValue          = 1.0
	dmPermission                   = false
	defaultMemberPermissions int64 = discordgo.PermissionManageServer

	commands = []*discordgo.ApplicationCommand{
		{
			Name: "ping",
			// All commands and options must have a description
			// Commands/options without description will fail the registration
			// of the command.
			Description: "Ping the bot to check ms latency",
		},
		{
			Name:        "bf4db",
			Description: "Command for searching players in BF4DB",
			Options: []*discordgo.ApplicationCommandOption{

				// Required options must be listed first since optional parameters
				// always come after when they're used.
				// The same concept applies to Discord's Slash-commands API

				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "discord-user",
					Description: "Search for players linked to a Discord User",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "global-search",
					Description: "Search players globally on BF4",
					Required:    false,
				},
			},
		},
	}

	commandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		"ping": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			startTime := time.Now()

			// Send initial response
			err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Testing ping...",
				},
			})
			if err != nil {
				log.Printf("Error sending initial response: %v", err)
				return
			}

			// Calculate response time
			responseTime := time.Since(startTime).Milliseconds()

			// Get API latency
			apiLatency := s.HeartbeatLatency().Milliseconds()

			// Edit the response with latency information
			newMsg := fmt.Sprintf("🏓 Pong!\nAPI: %dms\nBot: %dms", apiLatency, responseTime)
			_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content: &newMsg,
			})
			if err != nil {
				log.Printf("Error editing response: %v", err)
			}
		},

		"bf4db": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			// Access options in the order provided by the user.
			options := i.ApplicationCommandData().Options

			// Or convert the slice into a map
			optionMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))
			for _, opt := range options {
				optionMap[opt.Name] = opt
			}

			// This example stores the provided arguments in an []interface{}
			// which will be used to format the bot's response
			margs := make([]interface{}, 0, len(options))
			msgformat := ""

			// Get the value from the option map.
			// When the option exists, ok = true
			if option, ok := optionMap["global-search"]; ok {
				// Option values must be type asserted from interface{}.
				// Discordgo provides utility functions to make this simple.

				target := net.ParseIP(option.StringValue())
				if target == nil {
					margs = append(margs, option.StringValue())
					msgformat += "> Searching for: %s\n\n"
				} else {
					msgformat += "> Searching for: (Redacted)\n"
				}

				if results := GlobalSearch(option.StringValue(), BF4API); len(results.Data) == 0 {
					msgformat += "\n**Nenhuma conta encontrada**"
				} else {
					for _, v := range results.Data {
						chScore := fmt.Sprintf("%d", v.CheatScore)
						msgformat += v.Name + "\t|\tStatus: " + v.BanReason + "\t|\tCheat Score: " + chScore
						bf4dbLink := fmt.Sprint("https://bf4db.com/player/", v.ID)
						bfAgencyLink := fmt.Sprint("https://battlefield.agency/player/by-persona_id/bf4/", v.ID)
						msgformat += "\t|\t" + bf4dbLink + "\nBF Agency:\t" + bfAgencyLink + "\n\n"

					}
				}
			}

			if opt, ok := optionMap["discord-user"]; ok {
				margs = append(margs, opt.UserValue(nil).ID) // Here we call the BFDB
				log.Println("Requested user:", opt.UserValue(nil).ID)
				dcResults := DiscordSearch(opt.UserValue(nil).ID, BF4API)
				log.Println("Discord Results:", dcResults)

				msgformat += "> Usuario: <@%s> | Contas Encontradas:\n"
				if len(dcResults.Data) == 0 {
					msgformat += "\n**Nenhuma conta encontrada**"
				} else {
					for _, v := range dcResults.Data {
						chScore := fmt.Sprintf("%d", v.CheatScore)
						msgformat += v.Name + "\t|\tStatus: " + v.BanReason + "\t|\tCheat Score: " + chScore
						bf4dbLink := fmt.Sprint("https://bf4db.com/player/", v.PlayerId)
						bfAgencyLink := fmt.Sprint("https://battlefield.agency/player/by-persona_id/bf4/", v.PlayerId)
						msgformat += "\t|\t" + bf4dbLink + "\nBF Agency:\t" + bfAgencyLink + "\n\n"
					}
				}
			}

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: fmt.Sprintf(
						msgformat,
						margs...,
					),
				},
			})
		},
	}
)

func init() {
	s.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if h, ok := commandHandlers[i.ApplicationCommandData().Name]; ok {
			h(s, i)
		}
	})
}

func main() {
	s.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("Logged in as: %v#%v", s.State.User.Username, s.State.User.Discriminator)
	})
	err := s.Open()
	if err != nil {
		log.Fatalf("Cannot open the session: %v", err)
	}

	log.Println("Adding commands...")
	registeredCommands := make([]*discordgo.ApplicationCommand, len(commands))
	for i, v := range commands {
		cmd, err := s.ApplicationCommandCreate(s.State.User.ID, *GuildID, v)
		if err != nil {
			log.Panicf("Cannot create '%v' command: %v", v.Name, err)
		}
		registeredCommands[i] = cmd
	}

	defer s.Close()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	log.Println("Press Ctrl+C to exit")
	<-stop

	if *RemoveCommands {
		log.Println("Removing commands...")
		// // We need to fetch the commands, since deleting requires the command ID.
		// // We are doing this from the returned commands on line 375, because using
		// // this will delete all the commands, which might not be desirable, so we
		// // are deleting only the commands that we added.
		registeredCommands, err := s.ApplicationCommands(s.State.User.ID, *GuildID)
		if err != nil {
			log.Fatalf("Could not fetch registered commands: %v", err)
		}

		for _, v := range registeredCommands {
			err := s.ApplicationCommandDelete(s.State.User.ID, *GuildID, v.ID)
			if err != nil {
				log.Panicf("Cannot delete '%v' command: %v", v.Name, err)
			}
		}
	}

	log.Println("Gracefully shutting down.")
}
