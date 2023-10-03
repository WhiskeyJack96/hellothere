package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
)

//go:embed config.json
var configFile []byte

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

type config struct {
	NotificationChannelID string
	EmojiID               string
	RequiredRoleName      string

	requiredRoleID string
}

func run(_ context.Context) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource:   true,
		Level:       slog.LevelDebug,
		ReplaceAttr: nil,
	}))
	//load config
	m := map[string]config{}
	err := json.Unmarshal(configFile, &m)
	if err != nil {
		return err
	}

	//start a bot. args[1] should be the token for the bot.
	//bot needs permission to see presence, see users, see voice activity, and send messages
	session, err := discordgo.New("Bot " + os.Args[1])
	if err != nil {
		return err
	}

	//Add presence updates
	session.Identify.Intents = discordgo.IntentsAllWithoutPrivileged | discordgo.IntentGuildPresences
	session.AddHandler(func(s *discordgo.Session, m *discordgo.PresenceUpdate) {
		logger.Debug("presence update", slog.String("user", m.User.ID), slog.String("status", string(m.Status)))
	})

	//handle the ready event to prepare config object with guild specific info
	session.AddHandler(func(s *discordgo.Session, vs *discordgo.Ready) {
		logger.Debug("ready")
		for _, g := range vs.Guilds {
			guildConfig, err := registerGuild(s, g, m[g.ID])
			if err != nil {
				logger.Error("error registering guild", slog.String("err", err.Error()))
				return
			}
			fmt.Println(guildConfig.requiredRoleID)
			m[g.ID] = guildConfig
		}
	})

	session.AddHandler(func(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
		logger = logger.With(slog.String("username", vs.Member.User.Username), slog.String("guild", vs.GuildID), slog.String("channel", vs.ChannelID))

		logger.Info("joined")
		c, ok := m[vs.GuildID]
		if !ok {
			logger.Warn("unknown guild")
			return
		}

		if !shouldNotify(s, vs, logger, c) {
			return
		}

		message, err := buildNotificationMessage(c, vs, session)
		if err != nil {
			logger.Error("could not build message", slog.String("err", err.Error()))
			return
		}
		if _, err := session.ChannelMessageSend(c.NotificationChannelID, message); err != nil {
			logger.Error("could not sent message", slog.String("err", err.Error()))
			return
		}
	})

	err = session.Open()
	if err != nil {
		return err
	}

	fmt.Println("hello-there is now running.  Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
	// Cleanly close down the Discord session.
	return session.Close()
}

func shouldNotify(s *discordgo.Session, vs *discordgo.VoiceStateUpdate, logger *slog.Logger, c config) bool {
	//check if the user is just joining voice. This prevents mute/change channel/etc from triggering the notification
	if vs.BeforeUpdate != nil {
		logger.Debug("user already in a voice channel")
		return false
	}

	//check the users presence
	p, err := s.State.Presence(vs.GuildID, vs.UserID)
	if err != nil {
		logger.Warn("user presence could not be detected")
		return false
	}
	//Allow DND and invisible to be ignored
	if p.Status != discordgo.StatusOnline && p.Status != discordgo.StatusIdle {
		logger.Debug("user is incognito")
		return false
	}

	//Ensure the user has opted in to notifications by adopting the role
	if !userHasRole(vs.Member.Roles, c.requiredRoleID) {
		logger.Debug("user does not have role")
		return false
	}

	return true
}

func buildNotificationMessage(c config, vs *discordgo.VoiceStateUpdate, session *discordgo.Session) (string, error) {
	b := strings.Builder{}

	b.WriteString(c.EmojiID + " looks like ")
	if vs.Member.Nick != "" {
		b.WriteString(vs.Member.Nick)
	} else {
		b.WriteString(vs.Member.User.Username)
	}
	b.WriteString(" just joined ")

	channel, err := session.Channel(vs.ChannelID)
	if err != nil {
		return "", nil
	}

	b.WriteString(channel.Name)
	return b.String(), nil
}

func registerGuild(s *discordgo.Session, g *discordgo.Guild, guildConfig config) (config, error) {
	guild, err := s.Guild(g.ID)
	if err != nil {
		return config{}, err
	}
	for _, role := range guild.Roles {
		if role.Name == guildConfig.RequiredRoleName {
			guildConfig.requiredRoleID = role.ID
		}
	}
	return guildConfig, nil
}

func userHasRole(userRoleIDs []string, serverRoleID string) bool {
	return slices.Contains(userRoleIDs, serverRoleID)
}
