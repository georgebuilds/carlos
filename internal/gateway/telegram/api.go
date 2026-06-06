package telegram

import "encoding/json"

// This file contains the minimal slice of the Telegram Bot API wire
// schema we need to satisfy the gateway.Adapter contract: outbound
// sendMessage + answerCallbackQuery, inbound getUpdates streaming Update
// envelopes carrying Message or CallbackQuery.
//
// We define only the fields we read or write. The Bot API has many more
// fields than we model (entities, animations, voice, dice, etc.); they
// pass through to ignored JSON keys. If a future capability (file
// upload, photo inbound) needs a field, add it here rather than chasing
// a single big "complete" struct — the Bot API churns frequently and a
// tight slice keeps the surface area survivable.
//
// API reference: https://core.telegram.org/bots/api

// apiResponse is the envelope every Bot API method returns. Successful
// responses populate Result with the method-specific payload; failures
// populate Description plus, on rate-limited errors (429),
// Parameters.RetryAfter. Result is a json.RawMessage so each caller
// can decode it into the type it actually expects.
type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Parameters  *responseParams `json:"parameters,omitempty"`
}

// responseParams carries the per-error metadata Telegram returns. The
// only field we care about is RetryAfter — when present (alongside
// error_code 429), the adapter surfaces it so the broker's retry loop
// can wait the right amount of time.
type responseParams struct {
	RetryAfter int `json:"retry_after,omitempty"`
	// MigrateToChatID is set when a chat has been migrated to a
	// supergroup. We don't act on it but log it for visibility.
	MigrateToChatID int64 `json:"migrate_to_chat_id,omitempty"`
}

// sendMessageRequest is the body of POST /bot<token>/sendMessage.
// ChatID is mandatory; Text is the message body (already MarkdownV2-
// escaped if ParseMode is "MarkdownV2"). ReplyMarkup carries an inline
// keyboard for ApprovalRequest envelopes.
type sendMessageRequest struct {
	ChatID              int64                 `json:"chat_id"`
	Text                string                `json:"text"`
	ParseMode           string                `json:"parse_mode,omitempty"`
	DisableNotification bool                  `json:"disable_notification,omitempty"`
	ReplyMarkup         *inlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// inlineKeyboardMarkup is the on-message button grid. We render each
// Action as its own one-button row, matching how iOS Telegram lays out
// short button lists (each button gets the full row width, which keeps
// "Approve" / "Revise" / "Reject" legible on a phone).
type inlineKeyboardMarkup struct {
	InlineKeyboard [][]inlineKeyboardButton `json:"inline_keyboard"`
}

// inlineKeyboardButton models a single tap target. We only ever set Text
// + CallbackData; URL buttons, login buttons, etc. are out of scope.
type inlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

// answerCallbackQueryRequest dismisses the "loading" spinner Telegram
// shows on a tapped inline button. If we don't answer, the spinner
// hangs for 15s before the client gives up — bad UX even when the bot
// has already done the work behind the tap.
type answerCallbackQueryRequest struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
	ShowAlert       bool   `json:"show_alert,omitempty"`
}

// getUpdatesRequest configures the long-poll. Offset is the cursor:
// Telegram returns updates with update_id >= offset, and once we've
// processed update U we set offset = U+1 to advance.
//
// Timeout is the long-poll duration in seconds; Telegram holds the
// connection open up to that many seconds waiting for an update, which
// shaves request churn from the typical idle case.
type getUpdatesRequest struct {
	Offset         int64    `json:"offset,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

// update is one entry from the getUpdates stream. Either Message or
// CallbackQuery is populated for the inbound shapes we handle.
type update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *message       `json:"message,omitempty"`
	CallbackQuery *callbackQuery `json:"callback_query,omitempty"`
}

// message is the text-message inbound. We only read Chat.ID (for the
// whitelist check), Text (the body), and From (for the platform-native
// identity preserved in InboundEnvelope.From).
type message struct {
	MessageID int64  `json:"message_id"`
	Text      string `json:"text,omitempty"`
	Chat      chat   `json:"chat"`
	From      *user  `json:"from,omitempty"`
}

// callbackQuery is the inline-button-tap inbound. ID is what we feed
// back to answerCallbackQuery; Data is the callback_data string we
// encoded outbound (see callback.go).
type callbackQuery struct {
	ID      string   `json:"id"`
	From    user     `json:"from"`
	Message *message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// chat models the source chat. Telegram chats can be private (user DM),
// group, supergroup, or channel; we only model ID + Type because the
// whitelist check is the only consumer.
type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type,omitempty"`
}

// user models the sender. We capture ID + Username for diagnostic
// logging; the whitelist key is Chat.ID, not User.ID.
type user struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
	IsBot    bool   `json:"is_bot,omitempty"`
}

// sendMessageResponse is the typed payload of a successful sendMessage
// reply. We surface MessageID as the DeliveryReceipt.ProviderRef so the
// broker can correlate later if it needs to look up the message.
type sendMessageResponse struct {
	MessageID int64 `json:"message_id"`
	Chat      chat  `json:"chat"`
}
