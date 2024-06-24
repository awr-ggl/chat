// Package fcm implements push notification plugin for Google FCM backend.
// Push notifications for Android, iOS and web clients are sent through Google's Firebase Cloud Messaging service.
// Package fcm is push notification plugin using Google FCM.
// https://firebase.google.com/docs/cloud-messaging
package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	fbase "firebase.google.com/go"
	legacy "firebase.google.com/go/messaging"
	fcmv1 "google.golang.org/api/fcm/v1"

	"github.com/tinode/chat/server/logs"
	"github.com/tinode/chat/server/push"
	"github.com/tinode/chat/server/push/common"
	"github.com/tinode/chat/server/store/types"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

var handler Handler

const (
	// Size of the input channel buffer.
	bufferSize = 1024

	// The number of push messages sent in one batch. FCM constant.
	pushBatchSize = 100

	// The number of sub/unsub requests sent in one batch. FCM constant.
	subBatchSize = 1000
)

// Handler represents the push handler; implements push.PushHandler interface.
type Handler struct {
	input     chan *push.Receipt
	channel   chan *push.ChannelReq
	stop      chan bool
	projectID string

	client *legacy.Client
	v1     *fcmv1.Service
}

type configType struct {
	Enabled         bool            `json:"enabled"`
	DryRun          bool            `json:"dry_run"`
	Credentials     json.RawMessage `json:"credentials"`
	CredentialsFile string          `json:"credentials_file"`
	TimeToLive      int             `json:"time_to_live,omitempty"`
	ApnsBundleID    string          `json:"apns_bundle_id,omitempty"`
	Android         *common.Config  `json:"android,omitempty"`
	Apns            *common.Config  `json:"apns,omitempty"`
	Webpush         *common.Config  `json:"webpush,omitempty"`
}

// Init initializes the push handler
func (Handler) Init(jsonconf json.RawMessage) (bool, error) {

	var config configType
	err := json.Unmarshal([]byte(jsonconf), &config)
	if err != nil {
		return false, errors.New("failed to parse config: " + err.Error())
	}

	if !config.Enabled {
		return false, nil
	}

	if config.Credentials == nil && config.CredentialsFile != "" {
		config.Credentials, err = os.ReadFile(config.CredentialsFile)
		if err != nil {
			return false, err
		}
	}

	if config.Credentials == nil {
		return false, errors.New("missing credentials")
	}

	ctx := context.Background()
	credentials, err := google.CredentialsFromJSON(ctx, config.Credentials, "https://www.googleapis.com/auth/firebase.messaging")
	if err != nil {
		return false, err
	}
	if credentials.ProjectID == "" {
		return false, errors.New("missing project ID")
	}

	app, err := fbase.NewApp(ctx, &fbase.Config{}, option.WithCredentials(credentials))
	if err != nil {
		return false, err
	}

	handler.client, err = app.Messaging(ctx)
	if err != nil {
		return false, err
	}

	handler.v1, err = fcmv1.NewService(ctx, option.WithCredentials(credentials), option.WithScopes(fcmv1.FirebaseMessagingScope))
	if err != nil {
		return false, err
	}

	handler.input = make(chan *push.Receipt, bufferSize)
	handler.stop = make(chan bool, 1)
	handler.projectID = credentials.ProjectID

	// There used to be a goroutine for
	// the Channel for subscribing/unsubscribing devices to FCM topics.
	// Omitted temporarily as we don't need it for now.
	go func() {
		for {
			select {
			case rcpt := <-handler.input:
				go sendFcmV1(rcpt, &config)
			case <-handler.stop:
				return
			}
		}
	}()

	return true, nil
}

// send the jsonMsg to our custom fcm queue
//curl --location '34.101.108.131:8080/queues/groupchat_msg_fcm/jobs' \
// --header 'Content-Type: application/json' \
// --header 'Authorization: Bearer 1234' \
// --data '[
//     {
//         "name": "msg",
//         "data": {
//             "token": "fdstHnJKRqaisaeuvFpLDf:APA91bFgL3BdCk0egDq4slrJB8J_xYQd20-JLgyqK54IEY1TbtkFnmbr6BIzGjfUlWvaqAaIuGkrGB6RtrV7tqEHY4DeBg3Yu1wN0EWxkglwhND6qeeD9_-lzboh_Qb_DQBfaGanP36X",
//             "content": "from_server"
//         }
//     }
// ]'

func sendToQueue(jsonMsg string) {
	// send the jsonMsg to our custom fcm queue
	// [ { "name": "msg", "data": from jsonMsg]
	// TODO: set url from config
	// BullMQ url from Getenv (OS Environment Variable)
	var bullmq_proxy_url = os.Getenv("BULLMQ_PROXY_URL")
	var bullmq_proxy_token = os.Getenv("BULLMQ_PROXY_TOKEN")
	if bullmq_proxy_token == "" {
		bullmq_proxy_token = "1234"
	}
	url := bullmq_proxy_url + "/queues/groupchat_msg_fcm/jobs"
	body := []byte(fmt.Sprintf(`[{"name": "msg", "data": %s}]`, jsonMsg))

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		fmt.Println("Failed to create HTTP request:", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bullmq_proxy_token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Failed to send HTTP request:", err)
		return
	}
	fmt.Println("FCM sent to queue")

	defer resp.Body.Close()
}

func sendFcmV1(rcpt *push.Receipt, config *configType) {
	messages, uids := PrepareV1Notifications(rcpt, config)
	for i := range messages {
		// Create the FCM message request.
		jsonMsg, errJson := json.Marshal(messages[i])
		if errJson != nil {
			fmt.Println("Failed to marshal message to JSON:", errJson)
			continue
		}
		fmt.Println(uids)
		sendToQueue(string(jsonMsg))
		return
	}
}

func processSubscription(req *push.ChannelReq) {
	var channel string
	var devices []string
	var device string
	var channels []string

	if req.Channel != "" {
		devices = DevicesForUser(req.Uid)
		channel = req.Channel
	} else if req.DeviceID != "" {
		channels = ChannelsForUser(req.Uid)
		device = req.DeviceID
	}

	if (len(devices) == 0 && device == "") || (len(channels) == 0 && channel == "") {
		// No channels or devces to subscribe or unsubscribe.
		return
	}

	if len(devices) > subBatchSize {
		// It's extremely unlikely for a single user to have this many devices.
		devices = devices[0:subBatchSize]
		logs.Warn.Println("fcm: user", req.Uid.UserId(), "has more than", subBatchSize, "devices")
	}

	var err error
	var resp *legacy.TopicManagementResponse
	if channel != "" && len(devices) > 0 {
		if req.Unsub {
			resp, err = handler.client.UnsubscribeFromTopic(context.Background(), devices, channel)
		} else {
			resp, err = handler.client.SubscribeToTopic(context.Background(), devices, channel)
		}
		if err != nil {
			// Complete failure.
			logs.Warn.Println("fcm: sub or upsub failed", req.Unsub, err)
		} else {
			// Check for partial failure.
			handleSubErrors(resp, req.Uid, devices)
		}
		return
	}

	if device != "" && len(channels) > 0 {
		devices := []string{device}
		for _, channel := range channels {
			if req.Unsub {
				resp, err = handler.client.UnsubscribeFromTopic(context.Background(), devices, channel)
			} else {
				resp, err = handler.client.SubscribeToTopic(context.Background(), devices, channel)
			}
			if err != nil {
				// Complete failure.
				logs.Warn.Println("fcm: sub or upsub failed", req.Unsub, err)
				break
			}
			// Check for partial failure.
			handleSubErrors(resp, req.Uid, devices)
		}
		return
	}

	// Invalid request: either multiple channels & multiple devices (not supported) or no channels and no devices.
	logs.Err.Println("fcm: user", req.Uid.UserId(), "invalid combination of sub/unsub channels/devices",
		len(devices), len(channels))
}

func handleSubErrors(response *legacy.TopicManagementResponse, uid types.Uid, devices []string) {
	if response.FailureCount <= 0 {
		return
	}

	for _, errinfo := range response.Errors {
		// FCM documentation sucks. There is no list of possible errors so no action can be taken but logging.
		logs.Warn.Println("fcm sub/unsub error", errinfo.Reason, uid, devices[errinfo.Index])
	}
}

// IsReady checks if the push handler has been initialized.
func (Handler) IsReady() bool {
	return handler.input != nil
}

// Push returns a channel that the server will use to send messages to.
// If the adapter blocks, the message will be dropped.
func (Handler) Push() chan<- *push.Receipt {
	return handler.input
}

// Channel returns a channel for subscribing/unsubscribing devices to FCM topics.
func (Handler) Channel() chan<- *push.ChannelReq {
	return handler.channel
}

// Stop shuts down the handler
func (Handler) Stop() {
	handler.stop <- true
}

func init() {
	push.Register("fcm", &handler)
}
