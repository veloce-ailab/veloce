package service

import (
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
)

func TestParseOrderedUintListKeepsOrder(t *testing.T) {
	got := ParseOrderedUintList("7, 3,7,0,junk,1")
	want := []uint{7, 3, 1}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestJoinOrderedUintListKeepsOrder(t *testing.T) {
	if got := JoinOrderedUintList([]uint{5, 2, 5, 0, 9}); got != "5,2,9" {
		t.Fatalf("expected 5,2,9, got %q", got)
	}
}

func TestGroupCandidatesByUserChannel(t *testing.T) {
	channelOf := func(userChannelID uint, channelID uint) model.ModelConfig {
		id := userChannelID
		return model.ModelConfig{
			ChannelID: channelID,
			Channel:   model.Channel{ID: channelID, UserChannelID: &id},
		}
	}
	candidates := []model.ModelConfig{
		channelOf(2, 20),
		channelOf(1, 10),
		channelOf(2, 21),
	}

	groups := groupCandidatesByUserChannel(candidates, []uint{1, 3, 2})
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if groups[0][0].Channel.ID != 10 {
		t.Fatalf("expected first group to be user channel 1 (channel 10), got channel %d", groups[0][0].Channel.ID)
	}
	if len(groups[1]) != 2 || groups[1][0].Channel.ID != 20 || groups[1][1].Channel.ID != 21 {
		t.Fatalf("expected second group to keep candidate order for user channel 2, got %+v", groups[1])
	}

	single := groupCandidatesByUserChannel(candidates, []uint{2})
	if len(single) != 1 || len(single[0]) != 3 {
		t.Fatalf("expected single group with all candidates when one channel is bound, got %+v", single)
	}
}

func TestRequiredAPIKeyUserChannelsOrdered(t *testing.T) {
	apiKey := &model.APIKey{AllowedUserChannels: "9,4,6"}
	got, ok := requiredAPIKeyUserChannels(apiKey)
	if !ok {
		t.Fatalf("expected ok for multi-channel key")
	}
	want := []uint{9, 4, 6}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}

	if _, ok := requiredAPIKeyUserChannels(&model.APIKey{AllowedUserChannels: ""}); ok {
		t.Fatalf("expected not ok for key without channels")
	}
}
