package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

//go:embed config.json
var configFile []byte

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func isQuietHours() bool{
	current := time.Now().Hour();
	return current < 7 || current > 21
}

type config struct {
	NotificationChannelID string
	EmojiID               string
}

func run(ctx context.Context) error {
	session, err := discordgo.New("Bot " + os.Args[1])
	if err != nil {
		return err
	}
	m := map[string]config{}
	err = json.Unmarshal(configFile, &m)
	if err != nil {
		return err
	}

	session.AddHandler(func(s *discordgo.Session, vs *discordgo.Ready) {
		for _, g := range vs.Guilds {
			g, err := session.Guild(g.ID)
			if err != nil {
				fmt.Println("could not find guild", err)
				return
			}
			fmt.Println("registered to ", g.Name)
		}
		fmt.Println("ready", vs)
	})

	session.AddHandler(func(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
		fmt.Println("joined", vs.Member.User.Username, vs.GuildID, vs.ChannelID)
		if vs.BeforeUpdate != nil {
			return
		}
		if isQuietHours() {
			return
		}
		b := strings.Builder{}

		c, ok := m[vs.GuildID]
		if !ok {
			fmt.Println("unknown guild ", vs.GuildID)
		}
		b.WriteString(c.EmojiID + " looks like ")
		if vs.Member.Nick != "" {
			b.WriteString(vs.Member.Nick)
		} else {
			b.WriteString(vs.Member.User.Username)
		}
		b.WriteString(" just joined ")
		channel, err := session.Channel(vs.ChannelID)
		if err != nil {
			fmt.Println("could not find channel", err)
			return
		}
		b.WriteString(channel.Name)
		// session.ChannelMessageSend("1140034564438376580", b.String())
		session.ChannelMessageSend(c.NotificationChannelID, b.String())

	})

	err = session.Open()
	if err != nil {
		return err
	}

	fmt.Println("shadowfax is now running.  Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
	// Cleanly close down the Discord session.
	session.Close()
	return nil
}
