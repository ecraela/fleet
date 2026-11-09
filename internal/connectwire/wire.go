// Package connectwire defines the bounded outbound management protocol. It
// carries HAProxy browser operations, never sockets, host commands or node keys.
package connectwire

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const Path = "/ecraela.haproxy.connector.v1.Relay/Connect"
const MaxBody = 1 << 20
const MaxResponse = 8 << 20
const MaxFrame = 1500 << 10
const MaxRequests = 32

type Identity struct {
	UserID         string    `json:"user_id"`
	OrganizationID string    `json:"organization_id"`
	Capabilities   []string  `json:"capabilities"`
	ExpiresAt      time.Time `json:"expires_at"`
}
type Request struct {
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Query    string            `json:"query,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     []byte            `json:"body,omitempty"`
	Identity Identity          `json:"identity"`
}
type Frame struct {
	AllowManage    bool              `json:"allow_manage,omitempty"`
	Kind           string            `json:"kind"`
	ID             string            `json:"id,omitempty"`
	OrganizationID string            `json:"organization_id,omitempty"`
	Request        *Request          `json:"request,omitempty"`
	Status         int               `json:"status,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Data           []byte            `json:"data,omitempty"`
}

func Encode(f Frame) (*wrapperspb.BytesValue, error) {
	b, e := json.Marshal(f)
	if len(b) > MaxFrame {
		return nil, errors.New("connector frame too large")
	}
	return wrapperspb.Bytes(b), e
}
func Decode(b *wrapperspb.BytesValue) (Frame, error) {
	var f Frame
	if len(b.Value) > MaxFrame {
		return f, errors.New("connector frame too large")
	}
	err := json.Unmarshal(b.Value, &f)
	return f, err
}

type Sender interface{ SendMsg(any) error }

func Send(s Sender, f Frame) error {
	b, e := Encode(f)
	if e != nil {
		return e
	}
	return s.SendMsg(b)
}

type Receiver interface{ RecvMsg(any) error }

func Receive(s Receiver) (Frame, error) {
	b := new(wrapperspb.BytesValue)
	if e := s.RecvMsg(b); e != nil {
		return Frame{}, e
	}
	return Decode(b)
}

type Service interface{ Connect(grpc.ServerStream) error }

var Description = grpc.ServiceDesc{ServiceName: "ecraela.haproxy.connector.v1.Relay", HandlerType: (*Service)(nil), Streams: []grpc.StreamDesc{{StreamName: "Connect", ServerStreams: true, ClientStreams: true, Handler: func(s any, stream grpc.ServerStream) error { return s.(Service).Connect(stream) }}}}

const id = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`

var paths = regexp.MustCompile(`^/v1/(?:agent-setup|enrollment-status|clusters(?:/` + id + `/(?:enrollment-tokens|topology|events|incidents|rollouts|maintenance|certificates|listeners(?:/versions/` + id + `/rollback)?|applications(?:/status|/` + id + `/(?:traffic|requests|geography|traffic-rules|deploy|lifecycle|dns-intent|dns-delegation|versions/` + id + `/(?:rollback|traffic-rules)))?))?|fleet|nodes(?:/` + id + `/(?:metrics|history|events|revoke|discovery|operations|maintenance|configuration(?:/versions/` + id + `/rollback)?))?|enrollment-tokens)$`)
var softwareReleases = regexp.MustCompile(`^/v1/software/releases$`)
var softwareManagerRead = regexp.MustCompile(`^/v1/software/manager(?:/plan)?$`)
var softwareManagerCancel = regexp.MustCompile(`^/v1/software/manager/` + id + `/cancel$`)
var softwareNodeRead = regexp.MustCompile(`^/v1/nodes/` + id + `/software(?:/plan)?$`)
var softwareNodeCancel = regexp.MustCompile(`^/v1/nodes/` + id + `/software/` + id + `/cancel$`)

func Allowed(method, path string) bool {
	if softwareReleases.MatchString(path) || softwareManagerRead.MatchString(path) || softwareManagerCancel.MatchString(path) || softwareNodeRead.MatchString(path) || softwareNodeCancel.MatchString(path) {
		return method == "GET" && (softwareReleases.MatchString(path) || softwareManagerRead.MatchString(path) || softwareNodeRead.MatchString(path)) ||
			method == "POST" && (softwareManagerCancel.MatchString(path) || path == "/v1/software/manager" || softwareNodeCancel.MatchString(path) || softwareNodeRead.MatchString(path) && strings.HasSuffix(path, "/software"))
	}
	if (strings.HasSuffix(path, "/geography") || strings.HasSuffix(path, "/traffic-rules")) && method != "GET" {
		return false
	}
	return (method == "GET" || method == "POST" || method == "PUT" || method == "DELETE") && paths.MatchString(path)
}
