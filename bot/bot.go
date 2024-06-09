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
	fmt.Println("Bot is running")
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c

}

// TODO:
// 	use one channel for keeping the sounds and another for the commands,
//		the first one should delete any message after relevant use
// 	look into error handling in general
// 	try changing switch to something else, looks ugly af
//	look into Discord heartbeats
//  report "errors" to the user like "you're not in a voice channel" or "sound not found
//	"paginate" message search (each req is 100 each)m, maybe resend sound entry and delete old one to order by usage
//  cleanup entry output, probably url is enough
// 	look into discord ui for messages

func newMessage(discord *discordgo.Session, userMessage *discordgo.MessageCreate) {
	if userMessage.Author.ID == discord.State.User.ID {
		return
	}

	switch {
	// DEBUG
	case strings.Contains(userMessage.Content, ".status"):
		discord.MessageReactionAdd(userMessage.ChannelID, userMessage.ID, "✅")

	// UPLOAD A SOUND
	case len(userMessage.Attachments) != 0:
		fileName := userMessage.Attachments[0].Filename
		fileURL := userMessage.Attachments[0].URL
		messageID := userMessage.ID

		_, err := discord.ChannelMessageSend(userMessage.ChannelID, fmt.Sprintf("new-sound-entry\nname: %s\nurl: %s\nid: %s", fileName, fileURL, messageID))

		if err != nil {
			panic(err)
		}

	// SEARCH FOR A SOUND - should delete this eventually, not supposed to search by hand
	case strings.Contains(userMessage.Content, ".find"):
		searchTerm := strings.Split(userMessage.Content, ".find")[1]
		channelMessages, err := discord.ChannelMessages(userMessage.ChannelID, 100, "", "", "")

		if err != nil {
			panic(err)
		}

		soundEntryReference, err := findSound(channelMessages, searchTerm, userMessage.ID)
		if err != nil {
			panic(err)
		}
		discord.ChannelMessageSendReply(userMessage.ChannelID, "Found this", soundEntryReference)

		// CONNECT TO VOICE AND PLAY SOUND
	case strings.Contains(userMessage.Content, ".s"):
		soundName := strings.Split(userMessage.Content, ".s")[1]
		channelMessages, err := discord.ChannelMessages(userMessage.ChannelID, 100, "", "", "")

		fmt.Println("soundName: ", soundName)
		if err != nil {
			panic(err)
		}

		soundEntryReference, err := findSound(channelMessages, soundName, userMessage.ID)
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

		soundURL := strings.Split(channelMessage.Content, "url: ")[1]
		soundURL = strings.Split(soundURL, "\nid")[0]

		voiceState, err := discord.State.VoiceState(userMessage.GuildID, userMessage.Author.ID)
		if err != nil {
			panic(err)
		}

		currentChannel := voiceState.ChannelID
		voice, err := discord.ChannelVoiceJoin(userMessage.GuildID, currentChannel, false, false)
		if err != nil {
			panic(err)
		}

		PlayAudioFile(voice, soundURL)

	// DEBUG
	case strings.Contains(userMessage.Content, ".resetChannel"):
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
			playbackPosition := stream.PlaybackPosition()
			fmt.Printf("Playback: %10s, \r\n", playbackPosition)
		}
	}
}

// search for sound in channel messages
func findSound(channelMessages []*discordgo.Message, searchTerm string, messageID string) (*discordgo.MessageReference, error) {
	for _, channelMessage := range channelMessages {
		if CheckMessageForSoundEntry(channelMessage, searchTerm, messageID) {
			return channelMessage.Reference(), nil
		}
	}
	return nil, fmt.Errorf("sound not found")
}

// check if is a sound entry, search name, check it isn't the caller
func CheckMessageForSoundEntry(message *discordgo.Message, soundName string, messageID string) bool {
	return strings.Contains(message.Content, soundName) && message.ID != messageID && strings.Contains(message.Content, "new-sound-entry")
}
