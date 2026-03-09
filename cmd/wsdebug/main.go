package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func main() {
	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, "ws://localhost:7242/ws", nil)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.CloseNow()

	var msg map[string]interface{}
	if err := wsjson.Read(ctx, conn, &msg); err != nil {
		log.Fatal(err)
	}

	b, _ := json.MarshalIndent(msg, "", "  ")
	fmt.Println(string(b))
}
