package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

//go:embed config.json
var configFile []byte

var timeoutCorner sync.Map

const timeout = 5 * time.Minute

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

type RoleConfig struct {
	ManagementChannelID string
	MessageID           string

	EmojiRoleConfig map[string]string
}

func (rc RoleConfig) ReactionRelevant(logger *slog.Logger, er *discordgo.MessageReaction) (string, bool) {
	//If the emoji is of the proper kind on the proper message in the proper channel
	if er.ChannelID != rc.ManagementChannelID {
		logger.Debug("wrong channel")
		return "", false
	}
	if er.MessageID != rc.MessageID {
		logger.Debug("wrong message")
		return "", false
	}
	role, ok := rc.EmojiRoleConfig[er.Emoji.Name]
	if !ok {
		logger.Debug("unknown emoji")
		return "", false
	}
	return role, true
}

type GuildConfig struct {
	NotificationChannelID string
	EmojiID               string
	RequiredRoleName      string

	UserConfig map[string]UserConfig
	//emoji name to role name
	RoleConfig RoleConfig

	requiredRoleID string
}
type UserConfig struct {
	OnJoinSound string
}

type slashCommand struct {
	Description string
	Handler     func(s *discordgo.Session, i *discordgo.InteractionCreate)
}

type slashCommands map[string]slashCommand

func run(_ context.Context) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource:   true,
		Level:       slog.LevelDebug,
		ReplaceAttr: nil,
	}))
	//load config
	m := map[string]GuildConfig{}
	err := json.Unmarshal(configFile, &m)
	if err != nil {
		return err
	}
	fmt.Println(m)

	//start a bot. args[1] should be the token for the bot.
	//bot needs permission to see presence, see users, manage roles, see voice activity, and send messages
	//https://discord.com/oauth2/authorize?client_id=408164522067755008&permissions=39584871222336&integration_type=0&scope=bot
	session, err := discordgo.New("Bot " + os.Args[1])
	if err != nil {
		return err
	}

	//Add presence updates
	session.Identify.Intents = discordgo.IntentsAllWithoutPrivileged | discordgo.IntentGuildPresences
	session.AddHandler(func(s *discordgo.Session, m *discordgo.PresenceUpdate) {
		logger.Debug("presence update", slog.String("user", m.User.ID), slog.String("status", string(m.Status)))
	})

	//TODO refactor the handlers to be factory functions that take in the config/logger/etc and return the function
	commands := slashCommands{
		"voice-spam": {
			Description: "opts the user in to the voice-spam role",
			Handler: func(s *discordgo.Session, i *discordgo.InteractionCreate) {
				if err := s.GuildMemberRoleAdd(i.GuildID, i.Member.User.ID, m[i.GuildID].requiredRoleID); err != nil {
					logger.Error("could not add role to user", slog.String("err", err.Error()), slog.String("guild", i.GuildID), slog.String("user", i.Member.User.Username))
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
				if err := s.GuildMemberRoleRemove(i.GuildID, i.Member.User.ID, m[i.GuildID].requiredRoleID); err != nil {
					logger.Error("could not add role to user", slog.String("err", err.Error()), slog.String("guild", i.GuildID), slog.String("user", i.Member.User.Username))
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

	session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if h, ok := commands[i.ApplicationCommandData().Name]; ok {
			h.Handler(s, i)
		}
	})
	//handle the ready event to prepare config object with guild specific info
	session.AddHandler(func(s *discordgo.Session, vs *discordgo.Ready) {
		logger.Info("ready")
		for _, g := range vs.Guilds {
			guildConfig, err := registerGuild(s, g, m[g.ID])
			if err != nil {
				logger.Error("error registering guild",
					slog.String("guild", g.Name),
					slog.String("err", err.Error()),
				)
				return
			}

			//Register interactions
			for name, cmd := range commands {
				_, err := session.ApplicationCommandCreate(session.State.User.ID, g.ID, &discordgo.ApplicationCommand{Name: name, Description: cmd.Description})
				if err != nil {
					logger.Error("could not register command", slog.String("err", err.Error()))
				}
			}

			m[g.ID] = guildConfig

			request, err := session.Request(http.MethodGet, fmt.Sprintf("%s/%s", discordgo.EndpointGuild(g.ID), "soundboard-sounds"), nil)
			if err != nil {
				logger.Error("could not send message", slog.String("err", err.Error()))
			} else {
				logger.Debug("sounds:" + string(request))
			}
		}
	})
	session.AddHandler(func(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
		logger := logger.With(slog.String("username", vs.Member.User.Username), slog.String("guild", vs.GuildID), slog.String("channel", vs.ChannelID))

		logger.Info("joined")
		c, ok := m[vs.GuildID]
		if !ok {
			logger.Warn("unknown guild")
			return
		}
		logger.Info("joined", vs.Member.User.Username)
		//If the user is configured to play a sound then do that
		playSound(s, vs, logger, c.UserConfig[vs.Member.User.Username].OnJoinSound)

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

		timeoutCorner.Store(vs.UserID, true)
		time.AfterFunc(timeout, func() { timeoutCorner.Delete(vs.UserID) })
	})

	session.AddHandler(func(s *discordgo.Session, reactionAdd *discordgo.MessageReactionAdd) {
		reaction := reactionAdd.MessageReaction
		logger := reactionLogger(logger, reaction)
		guildConfig := m[reaction.GuildID]
		//If the emoji is of the proper kind on the proper message in the proper channel
		role, relevant := guildConfig.RoleConfig.ReactionRelevant(logger, reaction)
		if !relevant {
			return
		}
		err := s.GuildMemberRoleAdd(reaction.GuildID, reaction.UserID, role)
		if err != nil {
			logger.Error("failed to add role", slog.String("err", err.Error()))
			return
		}
		return
	})
	session.AddHandler(func(s *discordgo.Session, reactionRemove *discordgo.MessageReactionRemove) {
		reaction := reactionRemove.MessageReaction
		logger := reactionLogger(logger, reaction)
		guildConfig := m[reaction.GuildID]
		//If the emoji is of the proper kind on the proper message in the proper channel
		role, relevant := guildConfig.RoleConfig.ReactionRelevant(logger, reaction)
		if !relevant {
			return
		}
		err := s.GuildMemberRoleRemove(reaction.GuildID, reaction.UserID, role)
		if err != nil {
			logger.Error("failed to add role", slog.String("err", err.Error()))
			return
		}
		return
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

func reactionLogger(logger *slog.Logger, er *discordgo.MessageReaction) *slog.Logger {
	return logger.With(
		slog.String("channel", er.ChannelID),
		slog.String("message", er.MessageID),
		slog.String("emoji", er.Emoji.Name),
		slog.String("user", er.UserID),
	)
}

func playSound(s *discordgo.Session, vs *discordgo.VoiceStateUpdate, logger *slog.Logger, soundID string) {
	if soundID == "" {
		logger.Debug("user does not have a join sound configured")
		return
	}
	//check if the user is just joining voice. This prevents mute/change channel/etc from triggering the sound
	channelID := vs.ChannelID
	if vs.BeforeUpdate != nil && channelID == vs.BeforeUpdate.ChannelID {
		logger.Debug("user already in same channel")
		return
	}

	//in order to play a sound we must join the channel and not be muted
	vc, err := s.ChannelVoiceJoin(vs.GuildID, channelID, false, false)
	if err != nil {
		logger.Error("could not join voice channel", slog.String("err", err.Error()))
		return
	}
	defer vc.Close()

	//Then we post the sound! The sound should be from the same guild (or we need to update this to handle cross guild sounds)
	_, err = s.Request(http.MethodPost, fmt.Sprintf("%s/%s", discordgo.EndpointChannel(channelID), "send-soundboard-sound"), map[string]string{
		"sound_id": soundID,
	})
	if err != nil {
		logger.Error("could not send request", slog.String("err", err.Error()))
		return
	}
}

func shouldNotify(s *discordgo.Session, vs *discordgo.VoiceStateUpdate, logger *slog.Logger, c GuildConfig) bool {
	//skip bot users since we are a bot (and other bots are probably just spam)
	if vs.Member.User.Bot {
		return false
	}
	//check if the user is just joining voice. This prevents mute/change channel/etc from triggering the notification
	if vs.BeforeUpdate != nil {
		logger.Debug("user already in a voice channel")
		return false
	}

	//check quiet hours
	current := time.Now().Hour()
	if current < 8 || current > 22 {
		logger.Debug("quiet hours in effect")
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

	_, ok := timeoutCorner.Load(vs.UserID)
	if ok {
		logger.Debug("user already joined recently")
		return false
	}

	return true
}

func buildNotificationMessage(c GuildConfig, vs *discordgo.VoiceStateUpdate, session *discordgo.Session) (string, error) {
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

func registerGuild(s *discordgo.Session, g *discordgo.Guild, guildConfig GuildConfig) (GuildConfig, error) {
	guild, err := s.Guild(g.ID)
	if err != nil {
		return GuildConfig{}, err
	}

	roles := make(map[string]*discordgo.Role, len(guild.Emojis))
	for _, role := range guild.Roles {
		fmt.Println(role.Name)
		roles[role.Name] = role
	}
	for emojiName, roleName := range guildConfig.RoleConfig.EmojiRoleConfig {
		role, ok := roles[roleName]
		if !ok {
			return GuildConfig{}, fmt.Errorf("could not find role %s", roleName)
		}
		guildConfig.RoleConfig.EmojiRoleConfig[emojiName] = role.ID
	}

	role, ok := roles[guildConfig.RequiredRoleName]
	if ok {
		guildConfig.requiredRoleID = role.ID
	}
	return guildConfig, nil
}

func userHasRole(userRoleIDs []string, serverRoleID string) bool {
	return slices.Contains(userRoleIDs, serverRoleID)
}
