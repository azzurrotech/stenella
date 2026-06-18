package main

import (
	"stenella/storage"
	"stenella/ui"
)

func main() {
	store := storage.New("data/deck.xml")
	app := ui.New(store)
	app.Run()
}
