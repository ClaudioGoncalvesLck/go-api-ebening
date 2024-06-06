package bot

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jonas747/dca"
)

var BotToken string

func Run() {
	// create a session
	discord, err := discordgo.New("Bot " + BotToken)
	if err != nil {
		panic(err)
	}

	// add a event handler
	discord.AddHandler(newMessage)

	// open session
	discord.Open()
	defer discord.Close() // close session, after function termination

	// keep bot running untill there is NO os interruption (ctrl + C)
	fmt.Println("Bot running....")
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c

}

func newMessage(discord *discordgo.Session, message *discordgo.MessageCreate) {
	if message.Author.ID == discord.State.User.ID {
		return
	}

	switch {
	// DEBUG
	case strings.Contains(message.Content, ".status"):
		discord.MessageReactionAdd(message.ChannelID, message.ID, "✅")

	// UPLOAD A SOUND
	case len(message.Attachments) != 0:
		fileName := message.Attachments[0].Filename
		fileURL := message.Attachments[0].URL
		messageID := message.ID

		_, error := discord.ChannelMessageSend(message.ChannelID, fmt.Sprintf("new-sound-entry\nname: %s\nurl: %s\nid: %s", fileName, fileURL, messageID))

		if error != nil {
			fmt.Println(error)
		}

	// SEARCH FOR A SOUND - should delete this eventually, not supposed to search by hand
	case strings.Contains(message.Content, ".find"):
		searchTerm := strings.Split(message.Content, ".find")[1]
		messages, err := discord.ChannelMessages(message.ChannelID, 100, "", "", "")

		if err != nil {
			fmt.Println(err)
			return
		}

		// TODO: this is heavy, do something else
		for _, msg := range messages {
			if strings.Contains(msg.Content, searchTerm) && msg.ID != message.ID && strings.Contains(msg.Content, "new-sound-entry") {
				discord.ChannelMessageSend(message.ChannelID, msg.Content)
				return
			}
		}

	// CONNECT TO VOICE AND PLAY SOUND
	case strings.Contains(message.Content, ".s"):
		soundName := strings.Split(message.Content, ".s")[1]
		messages, err := discord.ChannelMessages(message.ChannelID, 100, "", "", "")

		if err != nil {
			// TODO: handle error properly
			fmt.Println(err)
			return
		}

		voice, err := discord.ChannelVoiceJoin(message.GuildID, "1118604071591493735", false, false)
		if err != nil {
			// TODO: handle error properly
			fmt.Println(err)
			return
		}
		defer voice.Close()

		// TODO: this is heavy, do something else
		for _, msg := range messages {
			// TODO: delete every message after relevant use and keep only files and upload entries
			if strings.Contains(msg.Content, soundName) && msg.ID != message.ID && strings.Contains(msg.Content, "new-sound-entry") {
				soundURL := strings.Split(msg.Content, "url: ")[1]
				soundURL = strings.Split(soundURL, "\nid")[0]

				PlayAudioFile(voice, soundURL)
				fmt.Println(soundURL)
				return
			}

		}

	// DEBUG
	case strings.Contains(message.Content, ".resetChannel"):
		allMessages, error := discord.ChannelMessages(message.ChannelID, 100, "", "", "")
		if error != nil {
			// TODO: handle error properly
			fmt.Println(error)
		}

		for _, msg := range allMessages {
			discord.ChannelMessageDelete(message.ChannelID, msg.ID)
		}
	}

}

// sample from github.com/jonas747/dca
func PlayAudioFile(v *discordgo.VoiceConnection, path string) {
	err := v.Speaking(true)
	if err != nil {
		log.Fatal("Failed setting speaking", err)
	}
	defer v.Speaking(false)

	opts := dca.StdEncodeOptions
	opts.RawOutput = true
	opts.Bitrate = 120

	encodeSession, err := dca.EncodeFile(path, opts)
	if err != nil {
		log.Fatal("Failed creating an encoding session: ", err)
	}

	done := make(chan error)
	stream := dca.NewStream(encodeSession, v, done)

	ticker := time.NewTicker(time.Second)

	// look into wtf this does
	for {
		select {
		case err := <-done:
			if err != nil && err != io.EOF {
				log.Fatal("An error occured", err)
			}

			// Clean up incase something happened and ffmpeg is still running
			encodeSession.Cleanup()
			return
		case <-ticker.C:
			stats := encodeSession.Stats()
			playbackPosition := stream.PlaybackPosition()

			fmt.Printf("Playback: %10s, Transcode Stats: Time: %5s, Size: %5dkB, Bitrate: %6.2fkB, Speed: %5.1fx\r", playbackPosition, stats.Duration.String(), stats.Size, stats.Bitrate, stats.Speed)
		}
	}
}
