package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

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

func DiscordSearch(player, api string) BFDiscord {
	myUrl := fmt.Sprint("https://bf4db.com/api/player/", player, "discordAccount/discord?api_token=", api)
	method := "GET"

	client := &http.Client{}
	req, err := http.NewRequest(method, myUrl, nil)

	if err != nil {
		fmt.Println(err)
		return BFDiscord{}
	}
	res, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return BFDiscord{}
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Println(err)
		return BFDiscord{}
	}
	var BFDc BFDiscord
	err = json.Unmarshal(body, &BFDc)
	if err != nil {
		fmt.Println("Unmarshal Error", err)
		return BFDiscord{}
	}
	if len(BFDc.Data) == 0 {
		fmt.Println(BFDc.Data, "No player found")
	}
	for _, v := range BFDc.Data {
		if v.BanReason == "" {
			v.BanReason = "Under Review"
		}
	}
	return BFDc
}

func GlobalSearch(player, api string) BFDBAPI {
	myUrl := fmt.Sprint("https://bf4db.com/api/player/", player, "/search?api_token=", api) // url with API key
	method := "GET"

	client := &http.Client{}
	req, err := http.NewRequest(method, myUrl, nil)

	if err != nil {
		fmt.Println("NewRequest Error")
		return BFDBAPI{}
	}
	res, err := client.Do(req)
	if err != nil {
		fmt.Println("Do Error")
		return BFDBAPI{}
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Println("ReadAll Error")
		return BFDBAPI{}
	}

	var bfdbApi BFDBAPI
	err = json.Unmarshal(body, &bfdbApi)
	if err != nil {
		fmt.Println("Unmarshal Error")
		return BFDBAPI{}
	}
	if len(bfdbApi.Data) == 0 {
		return BFDBAPI{}
	}

	for x := range bfdbApi.Data {
		if bfdbApi.Data[x].BanReason == "" {
			bfdbApi.Data[x].BanReason = "Under review"
		}
	}
	return bfdbApi
}
