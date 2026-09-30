package channel

import "testing"

func TestMessageIDs(t *testing.T) {
	message := SentMessage{ID: 2, IDs: []int64{1, 2}}
	if message.ID != 2 || len(message.IDs) != 2 {
		t.Fatalf("unexpected message: %+v", message)
	}
}
