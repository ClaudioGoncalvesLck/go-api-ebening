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
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jonas747/dca"
)

var BotToken string

var State BotState

type SoundName string
type UserID string

// SoundName as key bc that's what we're using for lookup
type SoundList map[SoundName]*Sound

type Sound struct {
	MessageID string
	URL       string
	Volume    string // dca uses 0-256 for some reason, try mapping it to 0-100 for better UX
}

type BotState struct {
	SoundList        SoundList
	Entrances        map[UserID]*Sound
	VoiceChannels    VoiceChannels
	VoiceConnections []*discordgo.VoiceConnection
}

type VoiceChannels struct {
	Channels []VoiceChannel
}
type VoiceChannel struct {
	ID             string
	GuildID        string
	Name           string
	UsersConnected []discordgo.User
}

var (
	playbackQueue = make(chan string, 10)
	stopPlayback  = make(chan bool)
	playbackMutex sync.Mutex
)

type Command string
type ChannelName string

const (
	SoundsChannel   ChannelName = "sounds"
	CommandsChannel ChannelName = "bot-commands"
)

const (
	PlaySound Command = ".s"
	SkipSound Command = ".ss"
	Connect   Command = ".c"
	Help      Command = ".help"
	List      Command = ".list"
)

func Run() {
	discord, err := discordgo.New("Bot " + BotToken)
	fmt.Println("Created discord session")
	if err != nil {
		panic(err)
	}

	State = BotState{
		// i dont think i need to do this
		SoundList: make(map[SoundName]*Sound),
		Entrances: make(map[UserID]*Sound),
		VoiceChannels: VoiceChannels{
			Channels: []VoiceChannel{},
		},
	}

	// add ready handler here to loads sounds to memory
	// for now use .list or force load on command

	discord.AddHandler(voiceStateUpdate)
	discord.AddHandler(newMessage)

	// open session
	err = discord.Open()
	if err != nil {
		panic(err)
	}
	defer discord.Close()

	// keep bot running untill there is NO os interruption (ctrl + C)
	fmt.Println("Bot is running")
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}

// TODO:
// 	look into error handling in general (remove panics and handle gracefully)
// 	look into discord ui for messages (commands and message components)
//  	https://discord.com/developers/docs/interactions/overview
// 	try improving zip upload
// 	implement setting a volume (maybe command gets a sound, bot reuploads with message like v:0-100)
// 	implement removing entrances
//  cache entrances and volumes too (assert only 1 entrace per user)
//  keep .help up to date
// 	add sound to state on upload
// 	maybe let commands be used in sounds channel but still delete them

func newMessage(discord *discordgo.Session, userMessage *discordgo.MessageCreate) {
	if userMessage.Author.Bot {
		return
	}

	channel, err := discord.Channel(userMessage.ChannelID)
	if err != nil {
		panic(err)
	}

	// DEV DEBUG
	if len(State.SoundList) == 0 {
		err := getSoundsRecursive(discord, userMessage.GuildID, "")
		if err != nil {
			panic(err)
		}
	}

	if channel.Name == string(SoundsChannel) {
		handleSoundsChannel(discord, userMessage)
	}

	if channel.Name == string(CommandsChannel) {
		handleCommandsChannel(discord, userMessage)
	}

}

// modified sample from github.com/jonas747/dca
func PlayAudioFile(v *discordgo.VoiceConnection, path string) {
	playbackMutex.Lock()
	defer playbackMutex.Unlock()

	err := v.Speaking(true)
	if err != nil {
		log.Fatal("Failed setting speaking", err)
	}

	opts := dca.StdEncodeOptions
	opts.RawOutput = true
	opts.Bitrate = 32

	encodeSession, err := dca.EncodeFile(path, opts)
	if err != nil {
		log.Fatal("Failed creating an encoding session: ", err)
	}

	done := make(chan error)
	dca.NewStream(encodeSession, v, done)

	for {
		select {
		case err := <-done:
			if err == dca.ErrVoiceConnClosed {
				encodeSession.Cleanup()
			} else {
				if err != nil && err != io.EOF {
					log.Fatal("An error occurred", err)
				}
			}

			v.Speaking(false)
			encodeSession.Cleanup()
			return
		case <-stopPlayback:
			v.Speaking(false)
			encodeSession.Cleanup()
			return
		}
	}
}

func tryConnectingToVoice(d *discordgo.Session, guildID string, userID string, channelID string) (*discordgo.VoiceConnection, error) {
	if userID == "" && channelID == "" {
		return nil, errors.New("specify either userID or channelID")
	}

	if channelID == "" {
		voiceState, err := d.State.VoiceState(guildID, userID)
		if err != nil {
			if err.Error() != "state cache not found" {
				return nil, err
			} else {
				return nil, nil
			}
		}
		channelID = voiceState.ChannelID
	}

	voice, err := d.ChannelVoiceJoin(guildID, channelID, false, false)
	if err != nil {
		return nil, err
	}

	return voice, nil
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

func handleSoundsChannel(discord *discordgo.Session, userMessage *discordgo.MessageCreate) {
	if len(userMessage.Attachments) > 0 {
		for _, attachment := range userMessage.Attachments {
			if strings.Split(attachment.Filename, ".")[1] == "zip" {
				handleZipUpload(discord, userMessage, attachment)
			}
		}
	} else {
		m, err := discord.ChannelMessageSendReply(userMessage.ChannelID, "Please use this channel for files only", userMessage.Reference())
		if err != nil {
			panic(err)
		}
		time.Sleep(3 * time.Second)
		discord.ChannelMessageDelete(userMessage.ChannelID, m.ID)
		discord.ChannelMessageDelete(userMessage.ChannelID, userMessage.ID)
	}
}

func handleCommandsChannel(discord *discordgo.Session, userMessage *discordgo.MessageCreate) {
	if len(userMessage.Attachments) > 0 {
		discord.ChannelMessageSendReply(userMessage.ChannelID, "If you're tring to upload a sound, put in in 'sounds' channel", userMessage.Reference())
	}

	// skips the current sound only if the message is exactly ".ss" to avoid accidental skips
	if userMessage.Content == string(SkipSound) {
		stopPlayback <- true
		return
	}

	command := strings.Split(userMessage.Content, " ")[0]
	switch {
	case command == string(Help):
		formattedMessage := "### **Note:** Always mention sounds by name without '.mp3'\n" +
			"**Commands:**\n" +
			"`.s (sound) <sound-name>`:\nPlays a sound or \n" +
			"`.c (connect)`:\nConnects to the voice channel you are in.\n" +
			"`.list`:\nLists all the sounds in the sounds channel." +
			"`.ss (skip sound)`:\nStops the current sound.\n"

		discord.ChannelMessageSend(userMessage.ChannelID, formattedMessage)

	case command == string(Connect):
		_, err := tryConnectingToVoice(discord, userMessage.GuildID, userMessage.Author.ID, "")
		if err != nil {
			panic(err)
		}

	case command == string(PlaySound):
		voice, err := tryConnectingToVoice(discord, userMessage.GuildID, userMessage.Author.ID, "")
		if err != nil {
			fmt.Println("Error connecting to voice channel:", err)
			panic(err)
		}
		if voice == nil {
			discord.ChannelMessageSend(userMessage.ChannelID, "You need to be in a voice channel")
			return
		}

		//lookup sound locally only, upload or bootup should assure it's either here or nowhere
		searchTerm := strings.Split(userMessage.Content, " ")[1]
		sound, ok := State.SoundList[SoundName(searchTerm)]
		if !ok {
			discord.ChannelMessageSend(userMessage.ChannelID, "Sound not found")
			return
		}

		// this probably shouldn't be here
		go PlaybackManager(voice)
		QueuePlayback(voice, sound.URL)

	case command == string(List):
		if len(State.SoundList) == 0 {
			err := getSoundsRecursive(discord, userMessage.GuildID, "")
			if err != nil {
				panic(err)
			}
		}

		// shoutout rasmussy
		listOutput := "```(" + fmt.Sprint(len(State.SoundList)) + ") " + "Available sounds :\n------------------\n\n"
		nb := 0
		for name := range State.SoundList {
			nb += 1
			var soundName = string(name)
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
				discord.ChannelMessageSend(userMessage.ChannelID, listOutput)
				listOutput = "```"
			}
		}
		listOutput += "```"
		if listOutput != "``````" {
			discord.ChannelMessageSend(userMessage.ChannelID, listOutput)
		}

	case command == ".rename":
		// find file by name, upload it with new name, delete old file
		searchTerm := strings.Split(userMessage.Content, " ")[1]
		newName := strings.Split(userMessage.Content, " ")[2]
		sound, ok := State.SoundList[SoundName(searchTerm)]
		if !ok {
			discord.ChannelMessageSend(userMessage.ChannelID, "Sound not found")
			return
		}

		soundsChannel := getSoundsChannelID(discord, userMessage.GuildID)
		_, err := discord.ChannelMessage(soundsChannel, sound.MessageID)
		if err != nil {
			panic(err)
		}

		discord.ChannelMessageDelete(soundsChannel, sound.MessageID)

		req, err := http.Get(sound.URL)
		if err != nil {
			panic(err)
		}
		defer req.Body.Close()

		message, err := discord.ChannelFileSend(soundsChannel, newName+".mp3", req.Body)
		if err != nil {
			panic(err)
		}

		// this is ugly af, check if there's a better way to do this
		sound.MessageID = message.ID
		State.SoundList[SoundName(newName)] = sound
		delete(State.SoundList, SoundName(searchTerm))

		discord.ChannelMessageSendReply(userMessage.ChannelID, "Sound renamed", userMessage.Reference())

	case command == ".addentrance":
		searchTerm := strings.Split(userMessage.Content, " ")[1]
		sound, ok := State.SoundList[SoundName(searchTerm)]
		if !ok {
			discord.ChannelMessageSend(userMessage.ChannelID, "Sound not found")
			return
		}

		soundsChannel := getSoundsChannelID(discord, userMessage.GuildID)
		soundMessage, err := discord.ChannelMessage(soundsChannel, sound.MessageID)
		if err != nil {
			panic(err)
		}

		if !soundMessage.Author.Bot {
			req, err := http.Get(sound.URL)
			if err != nil {
				panic(err)
			}
			defer req.Body.Close()

			soundMessage, err = discord.ChannelFileSend(soundsChannel, searchTerm+".mp3", req.Body)
			if err != nil {
				panic(err)
			}

			discord.ChannelMessageDelete(soundsChannel, sound.MessageID)
			State.SoundList[SoundName(searchTerm)] = &Sound{
				MessageID: soundMessage.ID,
				URL:       sound.URL,
				Volume:    sound.Volume,
			}
		}

		// right now a message tag can look like "e:userID;v:0-100;e:userID;"
		// where e: says that sound is an entrance to that user and v: is the volume for that sound
		if soundMessage.Content != "" {
			messageTags := strings.Split(soundMessage.Content, ";")
			if len(messageTags) > 0 {
				for _, tag := range messageTags {
					typeValue := strings.Split(tag, ":")
					tagType, tagValue := typeValue[0], typeValue[1]

					if tagType == "e" {
						if tagValue == userMessage.Author.ID {
							discord.ChannelMessageSend(userMessage.ChannelID, "This is already your entrance")
							return
						}
					}
				}

			}
		}

		soundMessage.Content += " e:" + userMessage.Author.ID + ";"
		_, err = discord.ChannelMessageEdit(soundsChannel, soundMessage.ID, soundMessage.Content)
		if err != nil {
			panic(err)
		}

		State.Entrances[UserID(userMessage.Author.ID)] = sound
	}
}

// This is most likely not the best way to do this
// if message has a zip file, extract it and send every .mp3 file to the sounds channel
// delete the original message and the files written to disk
func handleZipUpload(d *discordgo.Session, userMessage *discordgo.MessageCreate, attachment *discordgo.MessageAttachment) {
	err := os.Mkdir("sounds", 0755)
	if err != nil {
		if !os.IsExist(err) {
			panic(err)
		}
	}

	filePath := filepath.Join("sounds", attachment.Filename)
	downloadFile(attachment.Filename, attachment.URL)

	archive, err := zip.OpenReader(filePath)
	if err != nil {
		panic(err)
	}
	defer archive.Close()

	for _, file := range archive.File {
		if strings.Split(file.Name, ".")[1] == "mp3" {
			fileReader, err := file.Open()
			if err != nil {
				panic(err)
			}

			filePath := "sounds/" + file.Name
			fileWriter, err := os.Create(filePath)
			if err != nil {
				panic(err)
			}

			_, err = d.ChannelFileSend(userMessage.ChannelID, file.Name, fileReader)
			if err != nil {
				panic(err)
			}

			fileWriter.Close()
			fileReader.Close()
		}
	}

	err = os.RemoveAll("sounds")
	if err != nil {
		panic(err)
	}
	d.ChannelMessageDelete(userMessage.ChannelID, userMessage.ID)
}

// discord rate limit's at around 4/5 quick requests and this does 1 per 100 sounds (4 at the current 390 sounds)
func getSoundsRecursive(d *discordgo.Session, guildID string, beforeID string) error {
	soundsChannelID := getSoundsChannelID(d, guildID)
	if soundsChannelID == "" {
		return errors.New("sounds channel not found")
	}
	channelMessages, err := d.ChannelMessages(soundsChannelID, 100, beforeID, "", "")
	if err != nil {
		return err
	}

	for _, channelMessage := range channelMessages {
		trimmedName := strings.TrimSuffix(channelMessage.Attachments[0].Filename, ".mp3")

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
					State.Entrances[UserID(tagValue)] = sound
				}

				if tagType == "v" {
					// tagValue is the volume
					sound.Volume = tagValue
				}
			}

		}

		State.SoundList[SoundName(trimmedName)] = sound
	}

	// if length < 100, this is the last batch and checked all of them
	if len(channelMessages) < 100 {
		return nil
	}

	lastMessageID := channelMessages[len(channelMessages)-1].ID
	return getSoundsRecursive(d, guildID, lastMessageID)
}

func getSoundsChannelID(d *discordgo.Session, guildID string) string {
	channels, err := d.GuildChannels(guildID)
	if err != nil {
		panic(err)
	}

	for _, channel := range channels {
		if channel.Name == string(SoundsChannel) {
			return channel.ID
		}
	}

	return ""
}

func QueuePlayback(v *discordgo.VoiceConnection, path string) {
	select {
	case playbackQueue <- path:
	default:
		fmt.Println("Queue is full, discarding:", path)
	}
}

func PlaybackManager(v *discordgo.VoiceConnection) {
	for {
		select {
		case path := <-playbackQueue:
			PlayAudioFile(v, path)
		}
	}
}

// Redo this
func getUsersInVC(d *discordgo.Session, guildID string) VoiceChannels {
	currentGuild, err := d.State.Guild(guildID)
	if err != nil {
		fmt.Println("Error getting guild:", err)
		return VoiceChannels{}
	}

	voiceChannelsMap := make(map[string]*VoiceChannel)

	// Initialize the map with all voice channels in the guild
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

	// Populate users in each voice channel
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

	var voiceChannels VoiceChannels
	for _, vc := range voiceChannelsMap {
		voiceChannels.Channels = append(voiceChannels.Channels, *vc)
	}

	State.VoiceChannels = voiceChannels
	return voiceChannels
}

func voiceStateUpdate(d *discordgo.Session, v *discordgo.VoiceStateUpdate) {

	if len(State.VoiceChannels.Channels) == 0 {
		getUsersInVC(d, v.GuildID)
	} else {
		voiceChannelStateUpdate(d, v.UserID, v.ChannelID)

		// plays entrance if user joins a voice channel, doesn't on switch
		if v.ChannelID != "" && v.BeforeUpdate == nil {
			userEntrance, ok := State.Entrances[UserID(v.UserID)]
			if ok {
				voice, err := tryConnectingToVoice(d, v.GuildID, v.UserID, v.ChannelID)
				if err != nil {
					panic(err)
				}

				if voice == nil {
					return
				}

				go PlaybackManager(voice)
				QueuePlayback(voice, userEntrance.URL)
			}
		}

	}

}

func voiceChannelStateUpdate(d *discordgo.Session, userID string, channelID string) {
loop:
	for idx, vc := range State.VoiceChannels.Channels {
		for i, user := range vc.UsersConnected {
			if user.ID == userID {
				State.VoiceChannels.Channels[idx].UsersConnected = append(vc.UsersConnected[:i], vc.UsersConnected[i+1:]...)
				break loop
			}
		}
	}

	if channelID != "" {
		for idx, vc := range State.VoiceChannels.Channels {
			if vc.ID == channelID {
				user, err := d.User(userID)
				if err != nil {
					fmt.Println("Error getting user:", err)
					return
				}
				State.VoiceChannels.Channels[idx].UsersConnected = append(vc.UsersConnected, *user)
				return
			}
		}
	}
}
