package public

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/gogo/protobuf/proto"
	common "github.com/runconduit/conduit/controller/gen/common"
	healcheckPb "github.com/runconduit/conduit/controller/gen/common/healthcheck"
	pb "github.com/runconduit/conduit/controller/gen/public"
)

type mockGrpcServer struct {
	LastRequestReceived proto.Message
	ResponseToReturn    proto.Message
	TapStreamsToReturn  []*common.TapEvent
	ErrorToReturn       error
}

func (m *mockGrpcServer) Stat(ctx context.Context, req *pb.MetricRequest) (*pb.MetricResponse, error) {
	m.LastRequestReceived = req
	return m.ResponseToReturn.(*pb.MetricResponse), m.ErrorToReturn
}

func (m *mockGrpcServer) Version(ctx context.Context, req *pb.Empty) (*pb.VersionInfo, error) {
	m.LastRequestReceived = req
	return m.ResponseToReturn.(*pb.VersionInfo), m.ErrorToReturn
}

func (m *mockGrpcServer) ListPods(ctx context.Context, req *pb.Empty) (*pb.ListPodsResponse, error) {
	m.LastRequestReceived = req
	return m.ResponseToReturn.(*pb.ListPodsResponse), m.ErrorToReturn
}

func (m *mockGrpcServer) SelfCheck(ctx context.Context, req *healcheckPb.SelfCheckRequest) (*healcheckPb.SelfCheckResponse, error) {
	m.LastRequestReceived = req
	return m.ResponseToReturn.(*healcheckPb.SelfCheckResponse), m.ErrorToReturn
}

func (m *mockGrpcServer) Tap(req *pb.TapRequest, tapServer pb.Api_TapServer) error {
	m.LastRequestReceived = req
	if m.ErrorToReturn == nil {
		for _, msg := range m.TapStreamsToReturn {
			tapServer.Send(msg)
		}
	}

	return m.ErrorToReturn
}

type grpcCallTestCase struct {
	expectedRequest  proto.Message
	expectedResponse proto.Message
	functionCall     func() (proto.Message, error)
}

func TestServer(t *testing.T) {
	mockGrpcServer := &mockGrpcServer{}
	handler := &handler{
		grpcServer: mockGrpcServer,
	}

	httpServer := &http.Server{
		Addr:    ":8889",
		Handler: handler,
	}

	go func() {
		httpServer.ListenAndServe()
	}()
	defer httpServer.Shutdown(context.Background())

	client, err := NewInternalClient("localhost:8889")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	t.Run("Delegates all non-streaming RPC messages to the underlying grpc server", func(t *testing.T) {
		listPodsReq := &pb.Empty{}
		testListPods := grpcCallTestCase{
			expectedRequest: listPodsReq,
			expectedResponse: &pb.ListPodsResponse{
				Pods: []*pb.Pod{
					{Status: "ok-ish"},
				},
			},
			functionCall: func() (proto.Message, error) { return client.ListPods(context.TODO(), listPodsReq) },
		}

		statReq := &pb.MetricRequest{
			Summarize: false,
		}
		seriesToReturn := make([]*pb.MetricSeries, 0)
		for i := 0; i < 100; i++ {
			seriesToReturn = append(seriesToReturn, &pb.MetricSeries{Name: pb.MetricName_LATENCY, Metadata: &pb.MetricMetadata{Path: fmt.Sprintf("/%d", i)}})
		}
		testStat := grpcCallTestCase{
			expectedRequest: statReq,
			expectedResponse: &pb.MetricResponse{
				Metrics: seriesToReturn,
			},
			functionCall: func() (proto.Message, error) { return client.Stat(context.TODO(), statReq) },
		}

		versionReq := &pb.Empty{}
		testVersion := grpcCallTestCase{
			expectedRequest: versionReq,
			expectedResponse: &pb.VersionInfo{
				BuildDate: "02/21/1983",
			},
			functionCall: func() (proto.Message, error) { return client.Version(context.TODO(), versionReq) },
		}

		selfCheckReq := &healcheckPb.SelfCheckRequest{}
		testSelfCheck := grpcCallTestCase{
			expectedRequest: selfCheckReq,
			expectedResponse: &healcheckPb.SelfCheckResponse{
				Results: []*healcheckPb.CheckResult{
					{
						SubsystemName: "banana",
					},
				},
			},
			functionCall: func() (proto.Message, error) { return client.SelfCheck(context.TODO(), selfCheckReq) },
		}

		for _, testCase := range []grpcCallTestCase{testListPods, testStat, testVersion, testSelfCheck} {
			assertCallWasForwarded(t, mockGrpcServer, testCase.expectedRequest, testCase.expectedResponse, testCase.functionCall)
		}
	})

	t.Run("Delegates all streaming tap RPC messages to the underlying grpc server", func(t *testing.T) {
		expectedTapResponses := []*common.TapEvent{
			{
				Target: &common.TcpAddress{
					Port: 9999,
				},
				Source: &common.TcpAddress{
					Port: 6666,
				},
			}, {
				Target: &common.TcpAddress{
					Port: 2102,
				},
				Source: &common.TcpAddress{
					Port: 1983,
				},
			},
		}
		mockGrpcServer.TapStreamsToReturn = expectedTapResponses
		mockGrpcServer.ErrorToReturn = nil

		tapClient, err := client.Tap(context.TODO(), &pb.TapRequest{})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		for _, expectedTapEvent := range expectedTapResponses {
			actualTapEvent, err := tapClient.Recv()
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if !reflect.DeepEqual(actualTapEvent, expectedTapEvent) {
				t.Fatalf("Expecting tap event to be [%v], but was [%v]", expectedTapEvent, actualTapEvent)
			}
		}
	})

	t.Run("Handles errors before opening keep-alive response", func(t *testing.T) {
		mockGrpcServer.ErrorToReturn = errors.New("expected error")

		tapClient, err := client.Tap(context.TODO(), &pb.TapRequest{})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		_, err = tapClient.Recv()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

	})
}

func assertCallWasForwarded(t *testing.T, mockGrpcServer *mockGrpcServer, expectedRequest proto.Message, expectedResponse proto.Message, functionCall func() (proto.Message, error)) {
	mockGrpcServer.ErrorToReturn = nil
	mockGrpcServer.ResponseToReturn = expectedResponse
	actualResponse, err := functionCall()
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	actualRequest := mockGrpcServer.LastRequestReceived
	if !reflect.DeepEqual(actualRequest, expectedRequest) {
		t.Fatalf("Expecting server call to return [%v], but got [%v]", expectedRequest, actualRequest)
	}
	if !reflect.DeepEqual(actualResponse, expectedResponse) {
		t.Fatalf("Expecting server call to return [%v], but got [%v]", expectedResponse, actualResponse)
	}

	mockGrpcServer.ErrorToReturn = errors.New("expected")
	actualResponse, err = functionCall()
	if err == nil {
		t.Fatalf("Expecting error, got nothing")
	}
}
