package bot

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	dca "github.com/cgoncalveslck/dcalck"
)

var storeRebuilds int

var Token string
var store GlobalStore

type Command string

// SoundList [SoundName]
type SoundList map[string]*Sound

// Entrances [UserID]
type Entrances map[string]*Sound

type Sound struct {
	MessageID string `json:"messageId"`
	URL       string `json:"url"`
	Volume    int    `json:"volume"`
	// dca uses 0-256 for some reason, try mapping it to 0-100 for better UX // change this to uint8
}

type Channels struct {
	VoiceChannels []VoiceChannel `json:"voiceChannels"`
}

type GuildState struct {
	SoundList       SoundList `json:"soundList"`
	Entrances       Entrances `json:"entrances"`
	Channels        Channels  `json:"channels"`
	SoundsChannelID string    `json:"soundsChannelID"`
	Mutex           sync.Mutex
	StopPlayback    chan bool
}

// GlobalStore Store [guildID]
type GlobalStore map[string]*GuildState

type VoiceChannel struct {
	ID             string
	GuildID        string
	Name           string
	UsersConnected []discordgo.User
}

const (
	SoundsChannel   string = "sounds"
	CommandsChannel string = "bot-commands"
)

const (
	PlaySound   Command = ",s"
	SkipSound   Command = ",ss"
	Connect     Command = ",connect"
	Help        Command = ",help"
	List        Command = ",list"
	Rename      Command = ",rename"
	AddEntrance Command = ",addentrance"
	Adjustvol   Command = ",adjustvol"
	Find        Command = ",f"
)

func Run() {
	discord, err := discordgo.New("Bot " + Token)
	if err != nil {
		panic(err)
	}

	discord.AddHandler(readyHandler)
	discord.AddHandler(messageHandler)
	discord.AddHandler(voiceStateUpdate)

	err = discord.Open()
	if err != nil {
		panic(err)
	}

	// keep bot running until there is NO os interruption (ctrl + C)
	fmt.Println("Bot is running")
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}

// TODO:
// 	look into error handling in general (remove panics and handle gracefully)
// 	look into discord ui for messages (commands and message components)
//  	https://discord.com/developers/docs/interactions/overview
//  try improving zip upload
//  keep .help up to date
//  maybe let commands be used in sounds channel but still delete them
//  cleanup code repetition
//  improve rate limit optimization (apply commands locally, queue api calls(???))
// 	check if sound exists on upload
// 	order .list
//  profile mem with max load
// 	entrances stack if user leaves/rejoins and bot is playing (shouldn't happen but for future reference)

func messageHandler(d *discordgo.Session, userMsg *discordgo.MessageCreate) {
	if userMsg.Author.Bot {
		return
	}

	channel, err := d.Channel(userMsg.ChannelID)
	if err != nil {
		log.Fatalf("Error getting channel: %v", err)
	}

	if channel.Name == CommandsChannel {
		handleCommandsChannel(d, userMsg)
	}

	if channel.Name == SoundsChannel {
		handleSoundsChannel(d, userMsg)
	}

}

// PlayAudioFile modified sample from github.com/jonas747/dca
func PlayAudioFile(d *discordgo.Session, guildID string, v *discordgo.VoiceConnection, sound *Sound) {
	store[guildID].Mutex.Lock()
	defer store[guildID].Mutex.Unlock()

	select {
	case <-store[guildID].StopPlayback:
		fmt.Printf("Cleared existing stop signal for guild %s\n", guildID)
	default:
		fmt.Printf("No existing stop signal for guild %s\n", guildID)
	}

	opts := dca.StdEncodeOptions
	opts.RawOutput = true
	opts.Bitrate = 32
	opts.CompressionLevel = 5

	// use brain and redo this
	if sound.Volume == 0 {
		sound.Volume = 256
	} else {
		opts.Volume = sound.Volume
	}

	fmt.Println("Playing audio file")
	// Decode the dca file
	session, err := dca.EncodeFile(sound.URL, opts)
	if err != nil {
		fmt.Println("Error encoding file:", err)
		return
	}
	defer session.Cleanup()
	if v == nil || !v.Ready {
		fmt.Println("Voice not ready")
		v, err = d.ChannelVoiceJoin(guildID, store[guildID].SoundsChannelID, false, true)
		if err != nil {
			fmt.Println("Error joining voice channel:", err)
			return
		}
		for {
			fmt.Println("Waiting for voice to be ready")
			if v.Ready {
				fmt.Println("Voice ready")
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		err = v.Speaking(true)
		if err != nil {
			fmt.Println("Error setting speaking:", err)
			return
		}
	}

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-store[guildID].StopPlayback:
			time.Sleep(100 * time.Millisecond)
			return
		case <-ticker.C:
			frame, err := session.OpusFrame()

			if err != nil {
				fmt.Println("Error:", err)
				fmt.Println(session.FFMPEGMessages())
				if err != io.EOF {
					panic("Failed retrieving opus frame")
				}
				return
			}

			if frame == nil {
				fmt.Println("Frame is nil")
				return
			}

			v.OpusSend <- frame
		}
	}
}

func handleCommandsChannel(d *discordgo.Session, uMsg *discordgo.MessageCreate) {
	if len(uMsg.Attachments) > 0 {
		return
	}

	command := strings.Split(uMsg.Content, " ")[0]
	switch {
	case command == string(SkipSound):
		handleSkipSound(d, uMsg)
	case command == string(Help):
		formattedMessage :=
			"### To add sounds, just send them to the 'sounds' channel as a message (just the file, no text)\n" +
				"**Commands:**\n" +
				"`,s <sound-name>` Plays a sound\n" +
				"`,connect` Connects to the voice channel you are in.\n" +
				"`,list` Lists all sounds in the sounds channel.\n" +
				"`,ss` Stops the current sound.\n" +
				"`,ss <sound-name>` Skips current sound and plays new one.\n" +
				"`,rename <current-name> <new-name>` Renames a sound.\n" +
				"`,addentrance <sound-name>` Sets a sound as your entrance sound.\n" +
				"`,adjustvol <sound-name> <volume>` Adjusts the volume of a sound (0-512).\n" +
				"`,f <sound-name>` Finds a sound by name and returns a link to it."

		_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, formattedMessage)
		checkError(err)

	case command == string(Connect):
		voiceState, err := d.State.VoiceState(uMsg.Message.GuildID, uMsg.Author.ID)
		if err != nil {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Error getting voice state")
			checkError(err)
			return
		}

		v, err := d.ChannelVoiceJoin(uMsg.Message.GuildID, voiceState.ChannelID, false, false)
		if err != nil {
			panic(err)
		}

		if v == nil {
			_, err := d.ChannelMessageSendReply(uMsg.Message.ChannelID, "You need to be in a voice channel", uMsg.Reference())
			checkError(err)
			return
		}

	case command == string(Rename):
		sList := store[uMsg.Message.GuildID].SoundList
		// find file by name, upload it with new name, delete old file
		searchTerm := strings.Split(uMsg.Content, " ")[1]
		newName := strings.Split(uMsg.Content, " ")[2]
		sound, ok := sList[searchTerm]
		if !ok {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Sound not found")
			checkError(err)
			return
		}

		updatedMessage, updatedSound, err := reuploadSound(d, uMsg, sound, searchTerm, newName)
		if updatedMessage == nil || updatedSound == nil || err != nil {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Error re-uploading sound")
			if err != nil {
				panic(err)
			}
			return
		}

		// this is ugly af, check if there's a better way to do this
		sound.MessageID = updatedMessage.ID
		sList[newName] = updatedSound
		delete(sList, searchTerm)

		_, err = d.ChannelMessageSendReply(uMsg.Message.ChannelID, "Sound renamed", uMsg.Reference())
		checkError(err)

	case command == string(AddEntrance):
		sList := store[uMsg.Message.GuildID].SoundList
		searchTerm := strings.Split(uMsg.Content, " ")[1]

		sound, ok := sList[searchTerm]
		if !ok {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Sound not found")
			checkError(err)
			return
		}

		soundMessage, err := d.ChannelMessage(store[uMsg.GuildID].SoundsChannelID, sound.MessageID)
		if err != nil {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Error getting sound message")
			if err != nil {
				panic(err)
			}
			return
		}

		if !soundMessage.Author.Bot {
			updatedMessage, updatedSound, err := reuploadSound(d, uMsg, sound, searchTerm, "")
			if updatedMessage == nil || updatedSound == nil || err != nil {
				_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Error re-uploading sound")
				if err != nil {
					panic(err)
				}
				return
			}
			soundMessage = updatedMessage
			// updatedMessage here sometimes doesn't have GuildID??? idk why
			store[uMsg.Message.GuildID].SoundList[searchTerm] = updatedSound
			sound = updatedSound
		}

		userEntrance, ok := store[uMsg.Message.GuildID].Entrances[uMsg.Author.ID]
		if ok {
			if userEntrance.MessageID == sound.MessageID {
				_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "This is already your entrance")
				checkError(err)
				return
			} else {
				delete(store[uMsg.Message.GuildID].Entrances, uMsg.Author.ID)
				oldEntranceMessage, err := d.ChannelMessage(store[uMsg.GuildID].SoundsChannelID, userEntrance.MessageID)
				if err != nil {
					if strings.Contains(err.Error(), "HTTP 404") {
						delete(store[uMsg.Message.GuildID].Entrances, uMsg.Author.ID)
					} else {
						panic(err)
					}
				}

				// remove volume from message
				if oldEntranceMessage != nil && oldEntranceMessage.Content != "" {
					updatedTags := ""
					messageTags := strings.Split(oldEntranceMessage.Content, ";")
					for _, tag := range messageTags {
						if tag == "" {
							continue
						}

						typeValue := strings.Split(tag, ":")
						tagType := typeValue[0]

						if tagType == "e" {
							continue
						} else {
							updatedTags += tag + ";"
						}
					}

					oldEntranceMessage.Content = updatedTags
					_, err = d.ChannelMessageEdit(store[uMsg.Message.GuildID].SoundsChannelID, oldEntranceMessage.ID, oldEntranceMessage.Content)
					if err != nil {
						_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Error removing volume from old entrance")
						if err != nil {
							panic(err)
						}
						return
					}
				}
			}
		}

		// right now a message tag can look like "e:userID;v:0-100;e:userID;"
		// where e: says that sound is an entrance to that user and v: is the volume for that sound
		messageTags := strings.Split(soundMessage.Content, ";")
		if len(messageTags) > 0 {
			for _, tag := range messageTags {
				if tag == "" {
					continue
				}
				typeValue := strings.Split(tag, ":")
				tagType, tagValue := typeValue[0], typeValue[1]

				if tagType == "e" {
					if tagValue == uMsg.Author.ID {
						_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "This is already your entrance")
						checkError(err)
						return
					}
				}
			}

		}

		soundMessage.Content += "e:" + uMsg.Author.ID + ";"
		_, err = d.ChannelMessageEdit(store[uMsg.Message.GuildID].SoundsChannelID, soundMessage.ID, soundMessage.Content)
		checkError(err)

		store[uMsg.Message.GuildID].Entrances[uMsg.Author.ID] = sound
		_, err = d.ChannelMessageSendReply(uMsg.Message.ChannelID, "Entrance set", uMsg.Reference())
		checkError(err)

	case command == string(Adjustvol):
		searchTerm := strings.Split(uMsg.Content, " ")[1]
		volStr := strings.Split(uMsg.Content, " ")[2]
		volInt, err := strconv.ParseInt(volStr, 10, 64)
		if err != nil {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Volume must be between 1 and 512 (0-200%)")
			if err != nil {
				panic(err)
			}
		}

		if volInt < 0 || volInt > 512 {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Volume must be between 1 and 512 (0-200%)")
			checkError(err)
			return
		}

		sound, ok := store[uMsg.Message.GuildID].SoundList[searchTerm]
		if !ok {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Sound not found")
			checkError(err)
			return
		}

		soundMessage, err := d.ChannelMessage(store[uMsg.Message.GuildID].SoundsChannelID, sound.MessageID)
		if err != nil {
			panic(err)
		}

		// make function for this
		if !soundMessage.Author.Bot {
			updatedMessage, updatedSound, err := reuploadSound(d, uMsg, sound, searchTerm, "")
			if updatedMessage == nil || updatedSound == nil || err != nil {
				_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Error re-uploading sound")
				if err != nil {
					panic(err)
				}
				return
			}
			soundMessage = updatedMessage
			store[uMsg.Message.GuildID].SoundList[searchTerm] = updatedSound
			sound = updatedSound
		}

		updatedTags := ""
		if soundMessage.Content != "" {
			messageTags := strings.Split(soundMessage.Content, ";")
			for _, tag := range messageTags {
				if tag == "" {
					continue
				}

				typeValue := strings.Split(tag, ":")
				tagType := typeValue[0]

				if tagType == "v" {
					continue
				} else {
					updatedTags += tag + ";"
				}
			}
		}

		updatedTags += "v:" + volStr + ";"
		soundMessage.Content = updatedTags
		_, err = d.ChannelMessageEdit(store[uMsg.Message.GuildID].SoundsChannelID, soundMessage.ID, soundMessage.Content)
		if err != nil {
			panic(err)
		}
		store[uMsg.Message.GuildID].SoundList[searchTerm].Volume = int(volInt)
		_, err = d.ChannelMessageSendReply(uMsg.Message.ChannelID, "Volume adjusted", uMsg.Reference())
		checkError(err)
	case command == string(Find):
		searchTerm := strings.Split(uMsg.Content, " ")[1]
		sound, ok := store[uMsg.Message.GuildID].SoundList[searchTerm]
		if !ok {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Sound not found")
			checkError(err)
			return
		}

		messageLink := "https://discordapp.com/channels/" + uMsg.Message.GuildID + "/" + store[uMsg.Message.GuildID].SoundsChannelID + "/" + sound.MessageID
		messageMarkdown := "Found this: [" + searchTerm + "](" + messageLink + ")"
		_, err := d.ChannelMessageSendReply(uMsg.Message.ChannelID, messageMarkdown, uMsg.Reference())
		checkError(err)
	case command == string(PlaySound):
		if len(store[uMsg.Message.GuildID].SoundList) == 0 {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "No sounds loaded")
			checkError(err)
			return
		}

		voiceState, err := d.State.VoiceState(uMsg.Message.GuildID, uMsg.Author.ID)
		if err != nil {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Error getting voice state")
			checkError(err)
			return
		}

		channelID := voiceState.ChannelID
		voice, err := d.ChannelVoiceJoin(uMsg.Message.GuildID, channelID, false, false)
		if err != nil {
			panic(err)
		}

		if voice == nil {
			fmt.Println("Voice is nil outside")
			_, err := d.ChannelMessageSendReply(uMsg.Message.ChannelID, "You need to be in a voice channel", uMsg.Reference())
			checkError(err)
			return
		}

		//lookup sound locally only, upload or boot should assure it's either here or nowhere
		mSplit := strings.Split(uMsg.Content, " ")

		if len(mSplit) != 2 {
			fmt.Println("Invalid command")
			// TODO: handle this properly and call .help
			return
		}

		searchTerm := mSplit[1]
		sound, ok := store[uMsg.Message.GuildID].SoundList[searchTerm]
		if !ok {
			fmt.Println("Sound not found")
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Sound not found")
			checkError(err)
			return
		}

		go PlayAudioFile(d, uMsg.GuildID, voice, sound)

	case command == string(List):
		// shoutout rasmussy
		sList := store[uMsg.Message.GuildID].SoundList
		soundNames := make([]string, 0, len(sList))
		for name := range sList {
			soundNames = append(soundNames, name)
		}
		sort.Strings(soundNames)

		listOutput := "```(" + fmt.Sprint(len(sList)) + ") " + "Available sounds :\n------------------\n\n"
		nb := 0
		for _, name := range soundNames {
			nb += 1
			var soundName = name
			for len(soundName) < 15 {
				soundName += " "
			}
			listOutput += soundName + "\t"
			if nb%6 == 0 {
				listOutput += "\n"
			}
			// Discord max message length is 2000
			if len(listOutput) > 1950 { // removed condition for max sounds printed
				listOutput += "```"
				_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, listOutput)
				checkError(err)
				listOutput = "```"
			}
		}
		listOutput += "```"
		if listOutput != "``````" {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, listOutput)
			checkError(err)
		}
	}
}

// discord rate limit's at around 4/5 quick requests and this does 1 per 100 sounds (4 at the current 390 sounds)
// loads sounds and entrances to memory
func getSoundsRecursive(d *discordgo.Session, guildID string, beforeID string) error {
	fmt.Println("Getting sounds")
	soundsChannelID, err := getSoundsChannelID(d, guildID)
	checkError(err)
	channelMessages, err := d.ChannelMessages(soundsChannelID, 100, beforeID, "", "")
	if err != nil {
		return err
	}

	for _, channelMessage := range channelMessages {
		if len(channelMessage.Attachments) > 0 {
			fileName := channelMessage.Attachments[0].Filename
			if strings.Split(fileName, ".")[1] != "mp3" {
				continue
			}
			trimmedName := strings.TrimSuffix(fileName, ".mp3")

			sound := &Sound{
				MessageID: channelMessage.ID,
				URL:       channelMessage.Attachments[0].URL,
			}

			if channelMessage.Content != "" {
				messageTags := strings.Split(channelMessage.Content, ";")

				for _, tag := range messageTags {
					if tag == "" {
						continue
					}

					tag := strings.Split(tag, ":")
					tagType, tagValue := tag[0], tag[1]

					if tagType == "e" {
						// tagValue is the user ID
						store[guildID].Entrances[tagValue] = sound
					}

					if tagType == "v" {
						// tagValue is the volume
						volInt, err := strconv.ParseInt(tagValue, 10, 64)
						if err != nil {
							panic(err)
						}
						sound.Volume = int(volInt)
					}
				}
			}
			store[guildID].SoundList[trimmedName] = sound
		}
	}

	// if length < 100, this is the last batch and checked all of them
	if len(channelMessages) < 100 {
		return nil
	}

	lastMessageID := channelMessages[len(channelMessages)-1].ID
	return getSoundsRecursive(d, guildID, lastMessageID)
}

func getSoundsChannelID(d *discordgo.Session, guildID string) (string, error) {
	channels, err := d.GuildChannels(guildID)
	if err != nil {
		return "", err
	}

	for _, channel := range channels {
		if channel.Name == SoundsChannel {
			return channel.ID, nil
		}
	}

	return "", errors.New("no 'sounds' channel found")
}

func readyHandler(d *discordgo.Session, ready *discordgo.Ready) {
	fmt.Println("Bot is ready")

	buildStore(d, ready)
	fmt.Println("Store initialized")

	go maintainStore(d, ready)
}

func handleSkipSound(d *discordgo.Session, uMsg *discordgo.MessageCreate) {
	if store[uMsg.GuildID].StopPlayback != nil {
		select {
		case store[uMsg.GuildID].StopPlayback <- true:
			fmt.Println("Stopping playback")
		default:
			fmt.Println("Channel is full or closed")
		}
	}

	time.Sleep(500 * time.Millisecond)
	if len(strings.Split(uMsg.Content, " ")) > 1 {
		searchTerm := strings.Split(uMsg.Content, " ")[1]
		sound, ok := store[uMsg.Message.GuildID].SoundList[searchTerm]
		if !ok {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Sound not found")
			checkError(err)
			return
		}

		voiceState, err := d.State.VoiceState(uMsg.Message.GuildID, uMsg.Author.ID)
		if err != nil {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Error getting voice state")
			checkError(err)
			return
		}

		voice, err := d.ChannelVoiceJoin(uMsg.Message.GuildID, voiceState.ChannelID, false, false)
		go PlayAudioFile(d, uMsg.GuildID, voice, sound)
	}

}

func maintainStore(d *discordgo.Session, ready *discordgo.Ready) {
	rebuildTicker := time.NewTicker(4 * time.Hour)
	for range rebuildTicker.C {
		storeRebuilds++
		buildStore(d, ready)
	}
}

func buildStore(d *discordgo.Session, ready *discordgo.Ready) {
	store = make(GlobalStore)

	for _, guild := range ready.Guilds {
		soundsChannelID, err := getSoundsChannelID(d, guild.ID)
		if err != nil {
			panic(err)
		}

		store[guild.ID] = &GuildState{
			SoundList:       make(SoundList),
			Entrances:       make(Entrances),
			SoundsChannelID: soundsChannelID,
			Channels: Channels{
				VoiceChannels: []VoiceChannel{},
			},
			StopPlayback: make(chan bool, 1),
		}
	}

	for guildID := range store {
		err := getSoundsRecursive(d, guildID, "")
		checkError(err)
	}
}

// this is temporary, I'll make it work first then clean all this up
func checkError(err error) {
	if err != nil {
		panic(err)
	}
}

func getUsersInVC(d *discordgo.Session, guildID string) {
	currentGuild, err := d.State.Guild(guildID)
	if err != nil {
		panic(err)
	}

	voiceChannelsMap := make(map[string]*VoiceChannel)
	for _, channel := range currentGuild.Channels {
		if channel.Type == discordgo.ChannelTypeGuildVoice {
			voiceChannelsMap[channel.ID] = &VoiceChannel{
				ID:             channel.ID,
				GuildID:        guildID,
				Name:           channel.Name,
				UsersConnected: []discordgo.User{},
			}
		}
	}

	for _, vs := range currentGuild.VoiceStates {
		if vc, ok := voiceChannelsMap[vs.ChannelID]; ok {
			user, err := d.User(vs.UserID)
			if err != nil {
				fmt.Println("Error getting user:", err)
				continue
			}
			if !user.Bot {
				vc.UsersConnected = append(vc.UsersConnected, *user)
			}
		}
	}

	var Channels Channels
	for _, vc := range voiceChannelsMap {
		Channels.VoiceChannels = append(Channels.VoiceChannels, *vc)
	}

	// check if I actually have to do this
	guildState := store[guildID]
	guildState.Channels = Channels
	store[guildID] = guildState
}

func voiceStateUpdate(d *discordgo.Session, v *discordgo.VoiceStateUpdate) {
	if v.Member.User.Bot {
		if v.BeforeUpdate != nil && v.BeforeUpdate.ChannelID != "" {
			voiceChannelStateUpdate(d, v)
		}
		return
	}

	if len(store[v.GuildID].Channels.VoiceChannels) == 0 {
		getUsersInVC(d, v.GuildID)
	}

	voiceChannelStateUpdate(d, v)
	// plays entrance if user joins a voice channel, doesn't on switch
	if v.ChannelID != "" && v.BeforeUpdate == nil {
		userEntrance, ok := store[v.GuildID].Entrances[v.UserID]
		if ok {
			voice, err := d.ChannelVoiceJoin(v.GuildID, v.ChannelID, false, false)
			if err != nil {
				panic(err)
			}

			if voice == nil {
				return
			}

			// wait a sec for discord channel join sound etc
			time.Sleep(1 * time.Second)
			go PlayAudioFile(d, v.GuildID, voice, userEntrance)
		}
	}
}

func voiceChannelStateUpdate(d *discordgo.Session, v *discordgo.VoiceStateUpdate) {
	guildID := v.GuildID
	userID := v.UserID

	// Remove user from their previous channel (if any)
	for idx, vc := range store[guildID].Channels.VoiceChannels {
		for i, user := range vc.UsersConnected {
			if user.ID == userID {
				store[guildID].Channels.VoiceChannels[idx].UsersConnected = append(vc.UsersConnected[:i], vc.UsersConnected[i+1:]...)
				break
			}
		}
	}

	// Add user to their new channel (if they joined one)
	if v.ChannelID != "" {
		for idx, vc := range store[guildID].Channels.VoiceChannels {
			if vc.ID == v.ChannelID {
				user, err := d.User(userID)
				if err != nil {
					fmt.Println("Error getting user:", err)
					return
				}
				store[guildID].Channels.VoiceChannels[idx].UsersConnected = append(vc.UsersConnected, *user)
				return
			}
		}
	}
}

func reuploadSound(d *discordgo.Session, uMsg *discordgo.MessageCreate, sound *Sound, searchTerm string, fileName string) (*discordgo.Message, *Sound, error) {
	req, err := http.Get(sound.URL)
	if err != nil {
		return nil, nil, err
	}
	defer req.Body.Close()

	oldMessage, err := d.ChannelMessage(store[uMsg.Message.GuildID].SoundsChannelID, sound.MessageID)
	if err != nil {
		return nil, nil, err
	}

	if strings.Contains(oldMessage.Content, "e:") {
		for _, tag := range strings.Split(oldMessage.Content, ";") {
			if tag == "" {
				continue
			}

			typeValue := strings.Split(tag, ":")
			tagType, tagValue := typeValue[0], typeValue[1]

			if tagType == "e" {
				store[uMsg.Message.GuildID].Entrances[tagValue] = sound
			}
		}
	}

	if fileName == "" {
		fileName = searchTerm
	}

	soundMessage, err := d.ChannelMessageSendComplex(store[uMsg.Message.GuildID].SoundsChannelID, &discordgo.MessageSend{
		Content: oldMessage.Content,
		Files: []*discordgo.File{
			{
				Name:   fileName + ".mp3",
				Reader: req.Body,
			},
		},
	})
	if err != nil {
		return nil, nil, err
	}

	err = d.ChannelMessageDelete(store[uMsg.Message.GuildID].SoundsChannelID, sound.MessageID)
	if err != nil {
		return nil, nil, err
	}

	updatedSound := &Sound{
		MessageID: soundMessage.ID,
		URL:       soundMessage.Attachments[0].URL,
		Volume:    sound.Volume,
	}

	return soundMessage, updatedSound, nil
}

func downloadFile(filepath string, url string) (err error) {
	out, err := os.Create("sounds/" + filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

func handleSoundsChannel(d *discordgo.Session, uMsg *discordgo.MessageCreate) {
	if len(uMsg.Attachments) > 0 {
		for _, attachment := range uMsg.Attachments {
			if strings.Split(attachment.Filename, ".")[1] == "zip" {
				handleZipUpload(d, uMsg, attachment)
			}

			if strings.Split(attachment.Filename, ".")[1] == "mp3" {
				store[uMsg.Message.GuildID].SoundList[strings.TrimSuffix(attachment.Filename, ".mp3")] = &Sound{
					MessageID: uMsg.ID,
					URL:       attachment.URL,
				}
			}
		}
	} else {
		warningMsg, err := d.ChannelMessageSendReply(uMsg.Message.ChannelID, "Please use this channel for files only", uMsg.Reference())
		if err != nil {
			log.Fatalf("Error sending message: %v", err)
		}
		time.Sleep(3 * time.Second)
		err = d.ChannelMessageDelete(uMsg.Message.ChannelID, uMsg.ID)
		checkError(err)
		err = d.ChannelMessageDelete(uMsg.Message.ChannelID, warningMsg.ID)
		checkError(err)
	}
}

// // This is most likely not the best way to do this
// // if message has a zip file, extract it and send every .mp3 file to the sounds channel
// // delete the original message and the files written to disk
func handleZipUpload(d *discordgo.Session, uMsg *discordgo.MessageCreate, attachment *discordgo.MessageAttachment) {
	err := os.Mkdir("sounds", 0755)
	if err != nil {
		if !os.IsExist(err) {
			_, err := d.ChannelMessageSend(uMsg.Message.ChannelID, "Error handling zip upload")
			if err != nil {
				panic(err)
			}
			return
		}
	}

	filePath := filepath.Join("sounds", attachment.Filename)
	err = downloadFile(attachment.Filename, attachment.URL)
	checkError(err)

	archive, err := zip.OpenReader(filePath)
	if err != nil {
		panic(err)
	}
	defer archive.Close()

	for _, file := range archive.File {
		if strings.Split(file.Name, ".")[1] != "mp3" {
			return
		}

		fileReader, err := file.Open()
		if err != nil {
			panic(err)
		}

		filePath := "sounds/" + file.Name
		fileWriter, err := os.Create(filePath)
		if err != nil {
			panic(err)
		}

		_, err = d.ChannelFileSend(uMsg.Message.ChannelID, file.Name, fileReader)
		if err != nil {
			panic(err)
		}

		soundName := strings.TrimSuffix(file.Name, ".mp3")
		store[uMsg.GuildID].SoundList[soundName] = &Sound{
			MessageID: uMsg.ID,
			URL:       uMsg.Attachments[0].URL,
		}

		fileReader.Close()
		fileWriter.Close()
	}

	err = os.RemoveAll("sounds")
	checkError(err)

	err = d.ChannelMessageDelete(uMsg.Message.ChannelID, uMsg.ID)
	checkError(err)
}
