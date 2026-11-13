package scionic

import (
	"math/rand/v2"
	"slices"
	"testing"
)

func TestAnOrderedSetIsHeldAgainstASortedSlice(t *testing.T) {
	var set ordered[int]
	model := map[place]int{}
	draw := rand.New(rand.NewPCG(1, 2))
	check := func() {
		t.Helper()
		var want []place
		for at := range model {
			want = append(want, at)
		}
		slices.SortFunc(want, func(left, right place) int {
			switch {
			case left.before(right):
				return -1
			case right.before(left):
				return 1
			}
			return 0
		})
		var got []place
		for node := set.first(); node != nil; node = node.after() {
			if node.value != model[node.at] {
				t.Fatalf("%v holds %d, and not %d", node.at, node.value, model[node.at])
			}
			got = append(got, node.at)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("the order is %v, and not %v", got, want)
		}
		if set.len() != len(model) {
			t.Fatalf("%d are counted, and %d are kept", set.len(), len(model))
		}
	}
	for round := range 20_000 {
		at := place{major: draw.Uint64N(40), minor: draw.Uint64N(40)}
		switch draw.IntN(3) {
		case 0, 1:
			set.put(at, round)
			model[at] = round
		case 2:
			set.remove(at)
			delete(model, at)
		}
		value, found := set.get(at)
		if wanted, held := model[at]; found != held || value != wanted {
			t.Fatalf("round %d: %v is found as %d %v, and not %d %v", round, at, value, found, wanted, held)
		}
		if round%500 == 0 {
			check()
		}
	}
	check()
	// Emptied, it is empty.
	for at := range model {
		set.remove(at)
	}
	if set.first() != nil || set.len() != 0 {
		t.Fatal("an emptied set still holds something")
	}
}

func TestAnOrderedSetTakesPlacesInAnyOrder(t *testing.T) {
	const count = 50_000
	for _, name := range []string{"ascending", "descending", "shuffled"} {
		places := make([]place, count)
		for index := range places {
			places[index] = place{major: uint64(index / 7), minor: uint64(index % 7)}
		}
		switch name {
		case "descending":
			slices.Reverse(places)
		case "shuffled":
			draw := rand.New(rand.NewPCG(3, 4))
			draw.Shuffle(len(places), func(left, right int) {
				places[left], places[right] = places[right], places[left]
			})
		}
		var set ordered[struct{}]
		for _, at := range places {
			set.put(at, struct{}{})
		}
		index := 0
		for node := set.first(); node != nil; node = node.after() {
			if want := (place{major: uint64(index / 7), minor: uint64(index % 7)}); node.at != want {
				t.Fatalf("%s: place %d is %v, and not %v", name, index, node.at, want)
			}
			index++
		}
		if index != count {
			t.Fatalf("%s: %d places of %d", name, index, count)
		}
	}
}
