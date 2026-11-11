package telegram

// Update is one incoming event from getUpdates.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	EditedMessage *Message       `json:"edited_message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Message is a Telegram message (only the fields rex-tg uses).
type Message struct {
	MessageID       int64           `json:"message_id"`
	MessageThreadID int64           `json:"message_thread_id,omitempty"`
	IsTopicMessage  bool            `json:"is_topic_message,omitempty"`
	From            *User           `json:"from,omitempty"`
	Chat            Chat            `json:"chat"`
	Date            int64           `json:"date,omitempty"`
	Text            string          `json:"text,omitempty"`
	Caption         string          `json:"caption,omitempty"`
	Entities        []MessageEntity `json:"entities,omitempty"`
	Photo           []PhotoSize     `json:"photo,omitempty"`
	Document        *Document       `json:"document,omitempty"`
	Voice           *Voice          `json:"voice,omitempty"`
	Audio           *Audio          `json:"audio,omitempty"`
	Video           *Video          `json:"video,omitempty"`
	Sticker         *Sticker        `json:"sticker,omitempty"`
	ReplyToMessage  *Message        `json:"reply_to_message,omitempty"`
}

// MessageEntity marks up a span of message text.
type MessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
}

// Chat identifies a chat.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type,omitempty"`
}

// User is a Telegram user or bot.
type User struct {
	ID                        int64  `json:"id"`
	IsBot                     bool   `json:"is_bot,omitempty"`
	FirstName                 string `json:"first_name,omitempty"`
	Username                  string `json:"username,omitempty"`
	HasTopicsEnabled          bool   `json:"has_topics_enabled,omitempty"`
	AllowsUsersToCreateTopics bool   `json:"allows_users_to_create_topics,omitempty"`
}

// CallbackQuery is a press of an inline keyboard button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// ForumTopic is a topic created by CreateForumTopic.
type ForumTopic struct {
	MessageThreadID int64  `json:"message_thread_id"`
	Name            string `json:"name"`
}

// File is the result of GetFile.
type File struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
}

// PhotoSize is one size of a photo.
type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// Document is a general file.
type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

// Voice is a voice note.
type Voice struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
	Duration int    `json:"duration,omitempty"`
}

// Audio is an audio file.
type Audio struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
	Duration int    `json:"duration,omitempty"`
}

// Video is a video file.
type Video struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
	Duration int    `json:"duration,omitempty"`
}

// Sticker is a sticker.
type Sticker struct {
	FileID string `json:"file_id"`
	Emoji  string `json:"emoji,omitempty"`
}

// InlineKeyboardMarkup is an inline keyboard attached to a message.
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// InlineKeyboardButton is one button; set CallbackData or URL.
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// BotCommand is one entry for SetMyCommands.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SendMessageParams are the arguments of SendMessage.
// Link previews are always disabled.
type SendMessageParams struct {
	ChatID              int64
	MessageThreadID     int64
	Text                string
	ParseMode           string // "HTML" or ""
	DisableNotification bool
	ReplyMarkup         *InlineKeyboardMarkup
	ReplyToMessageID    int64
}

// EditMessageTextParams are the arguments of EditMessageText.
// Link previews are always disabled. A nil ReplyMarkup removes the keyboard.
type EditMessageTextParams struct {
	ChatID      int64
	MessageID   int64
	Text        string
	ParseMode   string // "HTML" or ""
	ReplyMarkup *InlineKeyboardMarkup
}
