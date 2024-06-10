package bot

import (
	"errors"
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
	fmt.Println("Bot is running")
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c

}

// TODO:
// 	use one channel for keeping the sounds and another for the commands,
//		(define a name for each channel so we can find it without harcoding ids)
//		the first one should delete any message after relevant use
// 	look into error handling in general
// 	try changing switchcase to something else, looks ugly af
//	look into Discord heartbeats
// 	look into discord ui for messages

func newMessage(discord *discordgo.Session, userMessage *discordgo.MessageCreate) {
	err := assertInitial(discord, userMessage)
	if err != nil {
		if err.Error() == "message is from bot" {
			return
		} else {
			panic(err)
		}
	}

	command := strings.Split(userMessage.Content, " ")[0]
	switch {
	// DEBUG
	case command == ".status":
		discord.MessageReactionAdd(userMessage.ChannelID, userMessage.ID, "✅")

	case command == ".help":
		formattedMessage := "**Note:** Always mention sounds by name without '.mp3'\n**Commands:**\n" +
			"`.find <sound-name>` - Finds the file of a sound by name.\n" +
			"`.s <sound-name>` - Plays the sound in the voice channel you are in.\n" +
			"`.resetChannel` - Deletes all messages in the channel.\n"

		fmt.Println(formattedMessage)
		discord.ChannelMessageSend(userMessage.ChannelID, formattedMessage)

	case command == ".find":
		searchTerm := strings.Split(userMessage.Content, " ")[1]
		soundEntryReference, err := findSoundRecursive(discord, userMessage, searchTerm, "")
		if err != nil {
			if err.Error() == "sound not found" {
				discord.ChannelMessageSend(userMessage.ChannelID, "sound not found")
			} else {
				panic(err)
			}
		}
		_, err = discord.ChannelMessageSendReply(userMessage.ChannelID, "Sound found", soundEntryReference)
		if err != nil {
			panic(err)
		}

	// CONNECT TO VOICE AND PLAY SOUND
	case command == ".s":
		searchTerm := strings.Split(userMessage.Content, " ")[1]
		soundEntryReference, err := findSoundRecursive(discord, userMessage, searchTerm, "")
		if err != nil {
			if err.Error() == "sound not found" {
				discord.ChannelMessageSend(userMessage.ChannelID, "sound not found")
			} else {
				panic(err)
			}
		}

		channelMessage, err := discord.ChannelMessage(userMessage.ChannelID, soundEntryReference.MessageID)
		if err != nil {
			panic(err)
		}

		soundURL := channelMessage.Attachments[0].URL
		voiceState, err := discord.State.VoiceState(userMessage.GuildID, userMessage.Author.ID)
		if err != nil || voiceState.ChannelID == "" {
			// err here is "state cache not found", idk wtf that means but it works :)
			discord.ChannelMessageSend(userMessage.ChannelID, "You need to be in a voice channel")
			return
		}

		currentChannel := voiceState.ChannelID
		voice, err := discord.ChannelVoiceJoin(userMessage.GuildID, currentChannel, false, false)
		if err != nil {
			panic(err)
		}

		PlayAudioFile(voice, soundURL)

	// DEBUG
	case command == ".resetChannel":
		allMessages, err := discord.ChannelMessages(userMessage.ChannelID, 100, "", "", "")
		if err != nil {
			panic(err)
		}

		// check if i can delete more than one message at a time
		for _, channelMessage := range allMessages {
			discord.ChannelMessageDelete(userMessage.ChannelID, channelMessage.ID)
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
			playbackPosition := stream.PlaybackPosition()
			fmt.Printf("Playback: %10s, \r\n", playbackPosition)
		}
	}
}

func findSoundRecursive(d *discordgo.Session, userMessage *discordgo.MessageCreate, searchTerm, beforeID string) (*discordgo.MessageReference, error) {
	channelMessages, err := d.ChannelMessages(userMessage.ChannelID, 100, beforeID, "", "")
	if err != nil {
		return nil, err
	}

	for _, channelMessage := range channelMessages {
		if len(channelMessage.Attachments) == 0 {
			continue
		}
		if strings.Split(channelMessage.Attachments[0].Filename, ".")[0] == searchTerm {
			return channelMessage.Reference(), nil
		}
	}

	// if length < 100, this is the last batch and checked all of them
	if len(channelMessages) < 100 {
		return nil, errors.New("sound not found")
	}

	lastMessageID := channelMessages[len(channelMessages)-1].ID
	return findSoundRecursive(d, userMessage, searchTerm, lastMessageID)
}

func assertInitial(d *discordgo.Session, userMessage *discordgo.MessageCreate) error {
	if userMessage.Author.ID == d.State.User.ID {
		return errors.New("message is from bot")
	}

	return nil
}
