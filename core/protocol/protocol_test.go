package protocol

import "testing"

func TestConversationOf(t *testing.T) {
	cases := []struct {
		name    string
		message ServerMessage
		want    string
		carries bool
	}{
		{"response", &Response{ConversationID: "C_1"}, "C_1", true},
		{"progress", &Progress{ConversationID: "C_2"}, "C_2", true},
		{"history page", &HistoryPage{ConversationID: "C_3"}, "C_3", true},
		{"rename", &ConversationRenamed{ConversationID: "C_4"}, "C_4", true},
		{"park", &ConversationParked{ConversationID: "C_5"}, "C_5", true},
		{"finalizing", &Finalizing{ConversationID: "C_6"}, "C_6", true},
		{"an empty id doesn't count", &Response{}, "", false},
		{"pong names none", &Pong{}, "", false},
		{"welcome is not routed", &Welcome{}, "", false},
	}
	for _, testCase := range cases {
		got, carries := ConversationOf(testCase.message)
		if got != testCase.want || carries != testCase.carries {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", testCase.name, got, carries, testCase.want, testCase.carries)
		}
	}
}
