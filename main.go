package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	api string = "774ac7a96e83ae855b604ca5799f0cbff93c115713c9916d059c2917e106f5dc"
)

var (
	wanted      = "_"
	playerFound = false
	IsDebug     = false
)

type BFDBAPI struct {
	Data []struct {
		Name       string    `json:"name"`
		IsBanned   int       `json:"is_banned"`
		BanReason  string    `json:"ban_reason"`
		CheatScore int       `json:"cheat_score"`
		CreatedAt  time.Time `json:"created_at"`
		UpdatedAt  time.Time `json:"updated_at"`
		ID         int       `json:"id"`
	} `json:"data"`
	Links struct {
		First string      `json:"first"`
		Last  string      `json:"last"`
		Prev  interface{} `json:"prev"`
		Next  interface{} `json:"next"`
	} `json:"links"`
	Meta struct {
		CurrentPage int    `json:"current_page"`
		From        int    `json:"from"`
		LastPage    int    `json:"last_page"`
		Path        string `json:"path"`
		PerPage     int    `json:"per_page"`
		To          int    `json:"to"`
		Total       int    `json:"total"`
	} `json:"meta"`
}

// G "https://bf4db.com/api/player/", "discordAccount/discord?api_token=", api

type BFDiscord struct {
	Data []struct {
		PlayerId   int       `json:"player_id"`
		Name       string    `json:"name"`
		IsBanned   int       `json:"is_banned"`
		BanReason  string    `json:"ban_reason"`
		EaGuid     string    `json:"ea_guid"`
		PbGuid     string    `json:"pb_guid"`
		CheatScore int       `json:"cheat_score"`
		CreatedAt  time.Time `json:"created_at"`
		UpdatedAt  time.Time `json:"updated_at"`
	} `json:"data"`
	UpdatedAt time.Time `json:"updated_at"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: bf4db <player name>")
		return
	}
	player := os.Args[1]

	if len(os.Args) > 2 {
		if os.Args[2] == "dbg" {
			IsDebug = true
		} else if os.Args[2] == "dc" {
			DiscordSearch(player, wanted)
			return
		}
	}
	fmt.Println("Searching for " + player + "\n")
	GlobalSearch(player, wanted)
}

func DiscordSearch(player, wanted string) {
	myUrl := fmt.Sprint("https://bf4db.com/api/player/", player, "discordAccount/discord?api_token=", api)
	method := "GET"

	client := &http.Client{}
	req, err := http.NewRequest(method, myUrl, nil)

	if err != nil {
		fmt.Println(err)
		return
	}
	res, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Println(err)
		return
	}
	var BFDc BFDiscord
	err = json.Unmarshal(body, &BFDc)
	if err != nil {
		fmt.Println("Unmarshal Error", err)
		if IsDebug { // if debug is enabled, print the response body
			if err.Error() == "invalid character '<' looking for beginning of value" {
				fmt.Println(string(body))
			}
		}
		return
	}
	if len(BFDc.Data) == 0 {
		fmt.Println(BFDc.Data, "No player found")
	}
	for _, v := range BFDc.Data {
		fmt.Println(v, "/n")
	}

	// Call DiscordMain
	DiscordMain(BFDBAPI{}, BFDc)
}

func GlobalSearch(player, wanted string) {
	myUrl := fmt.Sprint("https://bf4db.com/api/player/", player, "/search?api_token=", api) // url with API key
	method := "GET"

	client := &http.Client{}
	req, err := http.NewRequest(method, myUrl, nil)

	if err != nil {
		fmt.Println("NewRequest Error")
		return
	}
	res, err := client.Do(req)
	if err != nil {
		fmt.Println("Do Error")
		return
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Println("ReadAll Error")
		return
	}

	var bfdbApi BFDBAPI
	err = json.Unmarshal(body, &bfdbApi)
	if err != nil {
		fmt.Println("Unmarshal Error")
		if IsDebug { // if debug is enabled, print the response body
			if err.Error() == "invalid character '<' looking for beginning of value" {
				fmt.Println(string(body))
			}
		}
		return
	}
	if len(bfdbApi.Data) == 0 {
		if IsDebug {
			fmt.Println(bfdbApi.Data, "No player found") // FOR DEBUG ONLY
		}
		return
	}
	// print range of players when > 15
	if len(bfdbApi.Data) > 15 {
		fmt.Println("More than 15 players found! Total of", len(bfdbApi.Data), "\n")
	}
	for x := range bfdbApi.Data {
		if bfdbApi.Data[x].BanReason == "" {
			bfdbApi.Data[x].BanReason = "Under review"
		}

		if IsDebug == true { // FOR DEBUG ONLY
			fmt.Println("inside for:", bfdbApi.Data[x])
			continue
		}
		// if is nil, do nothing
		if a := bfdbApi.Data[x].ID; a == 0 {
			continue
		}
		if wanted != "_" { // FOR DEBUG ONLY
			if IsDebug == true {
				fmt.Println("IF wanted:", bfdbApi.Data[x])
				return
			}
			if bfdbApi.Data[x].Name == wanted {
				playerFound = true
				fmt.Println("\nFound " + wanted + " on IP " + player + "\n")
				GlobalSearch(player, "_")
				return
			}
		}
	}
}

func DiscordMain(PlayerR BFDBAPI, PlayerD BFDiscord) {
	if PlayerR.Data != nil {
		fmt.Println("PlayerR:", PlayerR)
	}

	if PlayerD.Data != nil {
		fmt.Println("PlayerD:", PlayerD)
	}
}
