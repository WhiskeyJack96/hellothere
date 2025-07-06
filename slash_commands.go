package main

import (
	"github.com/bwmarrin/discordgo"
	"log/slog"
)

type slashCommand struct {
	Description string
	Handler     func(s *discordgo.Session, i *discordgo.InteractionCreate)
}

type slashCommands map[string]slashCommand

func (c slashCommands) Register(s *discordgo.Session) {
	s.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if h, ok := c[i.ApplicationCommandData().Name]; ok {
			h.Handler(s, i)
		}
	})
}

func (c slashCommands) CreateCommands(s *discordgo.Session, config *botConfig) error {
	for guildID := range config.guilds {
		for name, cmd := range c {
			_, err := s.ApplicationCommandCreate(s.State.User.ID, guildID, &discordgo.ApplicationCommand{Name: name, Description: cmd.Description})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func newSlashCommands(config *botConfig) slashCommands {
	return slashCommands{
		"voice-spam": {
			Description: "opts the user in to the voice-spam role",
			Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
				gc := config.Get(i.GuildID)
				if err := s.GuildMemberRoleAdd(i.GuildID, i.Member.User.ID, gc.requiredRoleID); err != nil {
					gc.logger.Error("could not add role to user", slog.String("err", err.Error()), slog.String("guild", i.GuildID), slog.String("user", i.Member.User.Username))
					return
				}

				_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "Thou hast been granted \"hello-there\"",
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
			},
		},
		"no-spam": {
			Description: "opts the user out of the voice-spam role",
			Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
				gc := config.Get(i.GuildID)
				if err := s.GuildMemberRoleRemove(i.GuildID, i.Member.User.ID, gc.requiredRoleID); err != nil {
					gc.logger.Error("could not add role to user", slog.String("err", err.Error()), slog.String("guild", i.GuildID), slog.String("user", i.Member.User.Username))
					return
				}

				_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "Thou hast had thy privileges revoked",
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
			},
		},
	}
}
