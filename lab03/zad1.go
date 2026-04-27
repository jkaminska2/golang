package main

import (
	"errors"
	"fmt"
)

type Samolot struct {
	ID           int
	Model        string
	LiczbaMiejsc int
}

type Pasazer struct {
	ID   int
	Imie string
}

type Lot struct {
	ID         int
	Samolot    Samolot
	Skad       string
	Dokad      string
	Rezerwacje []Rezerwacja
}

func (l Lot) String() string {
	return "Lot " +
		fmt.Sprint(l.ID) +
		": " + l.Skad +
		" -> " + l.Dokad +
		" (Samolot: " + l.Samolot.Model +
		", miejsca: " + fmt.Sprint(l.Samolot.LiczbaMiejsc) + ")"
}

type Rezerwacja struct {
	Pasazer Pasazer
	LotID   int
}

type WyszukiwarkaLoty interface {
	SzukajLotowPoPorcie(port string) []Lot
}

type WyszukiwarkaRezerwacje interface {
	SzukajRezerwacjiPasazera(p Pasazer) []Rezerwacja
}

type SystemRezerwacji struct {
	Loty       []Lot
	Rezerwacje []Rezerwacja
}

func (s *SystemRezerwacji) Zarezerwuj(p Pasazer, lotID int) error {
	lot, err := s.znajdzLot(lotID)
	if err != nil {
		return err
	}

	for _, r := range s.Rezerwacje {
		if r.Pasazer.ID == p.ID && r.LotID == lotID {
			return errors.New("pasazer ma juz rezerwacje na ten lot")
		}
	}

	if s.liczbaRezerwacjiLotu(lotID) >= lot.Samolot.LiczbaMiejsc {
		return errors.New("brak wolnych miejsc")
	}

	r := Rezerwacja{Pasazer: p, LotID: lotID}
	s.Rezerwacje = append(s.Rezerwacje, r)
	return nil
}

func (s *SystemRezerwacji) Odwolaj(p Pasazer, lotID int) error {
	for i, r := range s.Rezerwacje {
		if r.Pasazer.ID == p.ID && r.LotID == lotID {
			s.Rezerwacje = append(s.Rezerwacje[:i], s.Rezerwacje[i+1:]...)
			return nil
		}
	}
	return errors.New("rezerwacja nie istnieje")
}

func (s *SystemRezerwacji) WolneMiejsca(lotID int) (int, error) {
	lot, err := s.znajdzLot(lotID)
	if err != nil {
		return 0, err
	}
	zajete := s.liczbaRezerwacjiLotu(lotID)
	return lot.Samolot.LiczbaMiejsc - zajete, nil
}

func (s *SystemRezerwacji) SzukajLotowPoPorcie(port string) []Lot {
	var wynik []Lot
	for _, l := range s.Loty {
		if l.Skad == port || l.Dokad == port {
			wynik = append(wynik, l)
		}
	}
	return wynik
}

func (s *SystemRezerwacji) SzukajRezerwacjiPasazera(p Pasazer) []Rezerwacja {
	var wynik []Rezerwacja
	for _, r := range s.Rezerwacje {
		if r.Pasazer.ID == p.ID {
			wynik = append(wynik, r)
		}
	}
	return wynik
}

func (s *SystemRezerwacji) znajdzLot(id int) (Lot, error) {
	for _, l := range s.Loty {
		if l.ID == id {
			return l, nil
		}
	}
	return Lot{}, errors.New("lot nie istnieje")
}

func (s *SystemRezerwacji) liczbaRezerwacjiLotu(lotID int) int {
	count := 0
	for _, r := range s.Rezerwacje {
		if r.LotID == lotID {
			count++
		}
	}
	return count
}

func main() {
	s1 := Samolot{ID: 1, Model: "Boeing", LiczbaMiejsc: 2}
	s2 := Samolot{ID: 2, Model: "Ryanair", LiczbaMiejsc: 3}

	l1 := Lot{ID: 1, Samolot: s1, Skad: "Gdansk", Dokad: "Krakow"}
	l2 := Lot{ID: 2, Samolot: s2, Skad: "Warszawa", Dokad: "Krakow"}

	p1 := Pasazer{ID: 1, Imie: "Asia"}
	p2 := Pasazer{ID: 2, Imie: "Beata"}

	system := SystemRezerwacji{
		Loty: []Lot{l1, l2},
	}

	fmt.Println(system.Zarezerwuj(p1, 1))
	fmt.Println(system.Zarezerwuj(p2, 1))
	fmt.Println(system.Zarezerwuj(p1, 1))

	wolne, _ := system.WolneMiejsca(1)
	fmt.Println("Lot 1 wolne miejsca:", wolne)

	fmt.Println(system.SzukajRezerwacjiPasazera(p1))

	fmt.Println(system.SzukajLotowPoPorcie("Krakow"))

	fmt.Println(system.Odwolaj(p1, 1))
	fmt.Println(system.SzukajRezerwacjiPasazera(p1))
}
