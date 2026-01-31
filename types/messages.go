package types

import (
	"encoding/json"
)

const (
	RoomJoined      = "room_joined"
	RoomLeft        = "room_left"
	RoomStateChange = "room_state_change"
	StatePatch      = "state_patch"
	Action          = "action"
	Error           = "error"
)

const (
	UnexpectedError      = 500
	MessageTooLarge      = 4003
	UnprocessableMessage = 4004
)

type Message struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type ErrorMessage struct {
	Code    uint32 `json:"code"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

var (
	ErrMessageTooLarge = ErrorMessage{
		Code:    MessageTooLarge,
		Message: "Message too large",
		Reason:  "Message payload exceeded default message buffer allocation",
		Fix:     "Trying sending a smaller message",
	}
	ErrUnprocessableMessage = ErrorMessage{
		Code:    UnprocessableMessage,
		Message: "Message is malformed",
		Reason:  "Message is malformed",
	}
	ErrUnexpected = ErrorMessage{
		Code:    UnexpectedError,
		Message: "Server error",
		Reason:  "Server error",
	}
)
