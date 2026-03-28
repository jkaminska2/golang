package main

import (
	"fmt"
	"math/rand"
	"time"
)

func main() {
	rand.Seed(time.Now().UnixNano())

	const trials = 1000
	const N = 10
	const k = 5

	stayWins := 0
	switchWins := 0

	for t := 0; t < trials; t++ {

		prize := rand.Intn(N)

		playerChoice := rand.Intn(N)

		opened := make([]bool, N)
		openedCount := 0

		for openedCount < k {
			x := rand.Intn(N)
			if x != prize && x != playerChoice && !opened[x] {
				opened[x] = true
				openedCount++
			}
		}

		if playerChoice == prize {
			stayWins++
		}

		var newChoice int
		for {
			newChoice = rand.Intn(N)
			if newChoice != playerChoice && !opened[newChoice] {
				break
			}
		}

		if newChoice == prize {
			switchWins++
		}
	}

	fmt.Println("Wyniki po", trials, "symulacjach:")
	fmt.Println("Pozostanie przy wyborze:", stayWins)
	fmt.Println("Zmiana wyboru:", switchWins)
}
