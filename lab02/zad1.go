package main

import (
	"fmt"
	"sort"
)

type Participant struct {
	Name       string
	Repertoire []Piece
	Scores     map[string][]int
}

type Piece struct {
	Name string
}

type ScoreStrategy interface {
	Average([]int) float64
}

type ChopinStrategy struct{}

func (ChopinStrategy) Average(scores []int) float64 {
	sort.Ints(scores)
	sum := 0
	for _, v := range scores[1 : len(scores)-1] {
		sum += v
	}
	return float64(sum) / float64(len(scores)-2)
}

func main() {
	pieces := []Piece{
		{"Polonez"},
		{"Etiuda"},
		{"Mazurki"},
	}

	p1 := Participant{"Anna", pieces, map[string][]int{}}
	p2 := Participant{"Jan", pieces, map[string][]int{}}
	p3 := Participant{"Maria", pieces, map[string][]int{}}

	p1.Scores[pieces[0].Name] = []int{20, 22, 25, 21, 23}
	p1.Scores[pieces[1].Name] = []int{18, 19, 20, 21, 22}
	p1.Scores[pieces[2].Name] = []int{23, 24, 25, 22, 21}

	p2.Scores[pieces[0].Name] = []int{21, 20, 19, 22, 23}
	p2.Scores[pieces[1].Name] = []int{17, 18, 19, 20, 21}
	p2.Scores[pieces[2].Name] = []int{24, 23, 22, 25, 24}

	p3.Scores[pieces[0].Name] = []int{25, 25, 24, 23, 22}
	p3.Scores[pieces[1].Name] = []int{20, 21, 22, 23, 24}
	p3.Scores[pieces[2].Name] = []int{19, 20, 21, 22, 23}

	participants := []Participant{p1, p2, p3}
	strategy := ChopinStrategy{}

	sort.Slice(participants, func(i, j int) bool {
		return total(participants[i], strategy) > total(participants[j], strategy)
	})

	fmt.Println("Ranking:")
	for _, p := range participants {
		fmt.Println(p.Name, total(p, strategy))
	}
}

func total(p Participant, s ScoreStrategy) float64 {
	sum := 0.0
	for _, scores := range p.Scores {
		sum += s.Average(scores)
	}
	return sum
}

// Ranking:
// Maria 67
// Anna 65
// Jan 63.66666666666667
