package types

type RoomState interface {
	GetState() any
	ApplyPatch(patch any) error
	Clone() RoomState
}
