package errs

import "fmt"

type Code int32

const (
	Success          Code = 0
	UserBanned       Code = 1001
	ContentIllegal   Code = 1002
	RateLimited      Code = 1003
	ChannelNotFound  Code = 1004
	NotInChannel     Code = 1005
	ChannelUnavail   Code = 1006
	MsgTooLong       Code = 1007
	AuthFailed       Code = 1008
	DuplicateMsg     Code = 1009
	ServerError      Code = 5000
)

type IMError struct {
	Code Code
	Msg  string
}

func (e *IMError) Error() string {
	return fmt.Sprintf("[%d] %s", e.Code, e.Msg)
}

func New(code Code, msg string) *IMError {
	return &IMError{Code: code, Msg: msg}
}

var (
	ErrUserBanned     = &IMError{UserBanned, "user is banned"}
	ErrContentIllegal = &IMError{ContentIllegal, "content is illegal"}
	ErrRateLimited    = &IMError{RateLimited, "rate limited"}
	ErrChannelNotFound = &IMError{ChannelNotFound, "channel not found"}
	ErrNotInChannel   = &IMError{NotInChannel, "user not in channel"}
	ErrChannelUnavail = &IMError{ChannelUnavail, "channel unavailable"}
	ErrMsgTooLong     = &IMError{MsgTooLong, "message too long"}
	ErrAuthFailed     = &IMError{AuthFailed, "authentication failed"}
	ErrDuplicateMsg   = &IMError{DuplicateMsg, "duplicate message"}
	ErrServerError    = &IMError{ServerError, "internal server error"}
)

// ToCode extracts the Code from an error. Returns ServerError for unknown errors.
func ToCode(err error) Code {
	if err == nil {
		return Success
	}
	if e, ok := err.(*IMError); ok {
		return e.Code
	}
	return ServerError
}

// ToMsg extracts the message from an error.
func ToMsg(err error) string {
	if err == nil {
		return "success"
	}
	if e, ok := err.(*IMError); ok {
		return e.Msg
	}
	return err.Error()
}
