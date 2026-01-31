package types

type RoomState interface {
	GetState() interface{}
	ApplyPatch(patch interface{}) error
	Clone() RoomState
}
