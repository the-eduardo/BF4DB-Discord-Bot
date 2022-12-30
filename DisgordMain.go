package main

import (
	"flag"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"log"
	"os"
	"os/signal"
)

// var (
// OwnBotToken = flag.String("token", "MTAyNzAxNTA0MTMyNjc4ODY1OQ.GJwoNR.nE2aooUvbe-qdQxUrG90iSKxV8ERtKpRj02E1M", "Bot token")
// AppID    = flag.String("app", "1027015041326788659", "Application ID")
// ChannelID = flag.String("ChannelID, "1025394880740073572", "Channel ID")
// ChannelID = "1025394880740073572"
// )

// Bot parameters
var (
	GuildID        = flag.String("guild", "", "Test guild ID. If not passed - bot registers commands globally")
	BotToken       = flag.String("token", "MTAyNzAxNTA0MTMyNjc4ODY1OQ.GJwoNR.nE2aooUvbe-qdQxUrG90iSKxV8ERtKpRj02E1M", "Bot access token")
	RemoveCommands = flag.Bool("rmcmd", true, "Remove all commands after shutdowning or not")
)

var s *discordgo.Session

func init() { flag.Parse() }

func init() {
	var err error
	s, err = discordgo.New("Bot " + *BotToken)
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
			Name: "basic-command",
			// All commands and options must have a description
			// Commands/options without description will fail the registration
			// of the command.
			Description: "Basic command",
		},
		{
			Name:                     "permission-overview",
			Description:              "Command for demonstration of default command permissions",
			DefaultMemberPermissions: &defaultMemberPermissions,
			DMPermission:             &dmPermission,
		},
		{
			Name:        "options",
			Description: "Command for demonstrating options",
			Options: []*discordgo.ApplicationCommandOption{

				// Required options must be listed first since optional parameters
				// always come after when they're used.
				// The same concept applies to Discord's Slash-commands API
				
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user-option",
					Description: "User option",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionRole,
					Name:        "role-option",
					Description: "Role option",
					Required:    false,
				},
			},
		},
	}

	commandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		"basic-command": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Hey there! Congratulations, you just executed your first slash command",
				},
			})
		},
		"options": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
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
			msgformat := "You learned how to use command options! " +
				"Take a look at the value(s) you entered:\n"

			// Get the value from the option map.
			// When the option exists, ok = true
			if option, ok := optionMap["string-option"]; ok {
				// Option values must be type asserted from interface{}.
				// Discordgo provides utility functions to make this simple.
				margs = append(margs, option.StringValue())
				msgformat += "> string-option: %s\n"
			}

			if opt, ok := optionMap["integer-option"]; ok {
				margs = append(margs, opt.IntValue())
				msgformat += "> integer-option: %d\n"
			}

			if opt, ok := optionMap["number-option"]; ok {
				margs = append(margs, opt.FloatValue())
				msgformat += "> number-option: %f\n"
			}

			if opt, ok := optionMap["bool-option"]; ok {
				margs = append(margs, opt.BoolValue())
				msgformat += "> bool-option: %v\n"
			}

			if opt, ok := optionMap["channel-option"]; ok {
				margs = append(margs, opt.ChannelValue(nil).ID)
				msgformat += "> channel-option: <#%s>\n"
			}

			if opt, ok := optionMap["user-option"]; ok {
				margs = append(margs, opt.UserValue(nil).ID)
				msgformat += "> user-option: <@%s>\n"
			}

			if opt, ok := optionMap["role-option"]; ok {
				margs = append(margs, opt.RoleValue(nil, "").ID)
				msgformat += "> role-option: <@&%s>\n"
			}

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				// Ignore type for now, they will be discussed in "responses"
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
		// registeredCommands, err := s.ApplicationCommands(s.State.User.ID, *GuildID)
		// if err != nil {
		// 	log.Fatalf("Could not fetch registered commands: %v", err)
		// }

		for _, v := range registeredCommands {
			err := s.ApplicationCommandDelete(s.State.User.ID, *GuildID, v.ID)
			if err != nil {
				log.Panicf("Cannot delete '%v' command: %v", v.Name, err)
			}
		}
	}

	log.Println("Gracefully shutting down.")
}
