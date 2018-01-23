package public

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/golang/protobuf/proto"
	pb "github.com/runconduit/conduit/controller/gen/public"
	log "github.com/sirupsen/logrus"
)

const (
	errorHeader                = "conduit-error"
	defaultHttpErrorStatusCode = http.StatusInternalServerError
	contentTypeHeader          = "Content-Type"
	protobufContentType        = "application/octet-stream"
	numBytesForMessageLength   = 4
)

type httpError struct {
	Code         int
	WrappedError error
}

type flushableResponseWriter interface {
	http.ResponseWriter
	http.Flusher
}

func (e httpError) Error() string {
	return fmt.Sprintf("HTTP error, status Code [%d], wrapped error is: %v", e.Code, e.WrappedError)
}

func httpRequestToProto(req *http.Request, protoRequestOut proto.Message) error {
	bytes, err := ioutil.ReadAll(req.Body)
	if err != nil {
		return httpError{
			Code:         http.StatusBadRequest,
			WrappedError: err,
		}
	}

	err = proto.Unmarshal(bytes, protoRequestOut)
	if err != nil {
		return httpError{
			Code:         http.StatusBadRequest,
			WrappedError: err,
		}
	}

	return nil
}

func writeErrorToHttpResponse(w http.ResponseWriter, errorObtained error) {
	statusCode := defaultHttpErrorStatusCode
	errorToReturn := errorObtained

	if httpErr, ok := errorObtained.(httpError); ok {
		statusCode = httpErr.Code
		errorToReturn = httpErr.WrappedError
	}

	w.Header().Set(errorHeader, http.StatusText(statusCode))

	errorAsProto := &pb.ApiError{Error: errorToReturn.Error()}

	err := writeProtoToHttpResponse(w, errorAsProto)
	if err != nil {
		log.Errorf("Error writing error to http response: %v", err)
		w.Header().Set(errorHeader, err.Error())
	}
}

func writeProtoToHttpResponse(w http.ResponseWriter, msg proto.Message) error {
	w.Header().Set(contentTypeHeader, protobufContentType)
	marshalledProtobufMessage, err := proto.Marshal(msg)
	if err != nil {
		return err
	}

	fullPayload, err := serializeAsPayload(marshalledProtobufMessage)
	if err != nil {
		return err
	}
	_, err = w.Write(fullPayload)
	return err
}

func newStreamingWriter(w http.ResponseWriter) (flushableResponseWriter, error) {
	flushableWriter, ok := w.(flushableResponseWriter)
	if !ok {
		return nil, fmt.Errorf("streaming not supported by this writer")
	}

	flushableWriter.Header().Set("Connection", "keep-alive")
	flushableWriter.Header().Set("Transfer-Encoding", "chunked")
	return flushableWriter, nil
}

func serializeAsPayload(messageContentsInBytes []byte) ([]byte, error) {
	lengthOfThePayload := uint32(len(messageContentsInBytes))

	messageLengthInBytes := make([]byte, numBytesForMessageLength)
	binary.LittleEndian.PutUint32(messageLengthInBytes, lengthOfThePayload)

	return append(messageLengthInBytes, messageContentsInBytes...), nil
}

func deserializePayloadFromReader(reader *bufio.Reader) ([]byte, error) {
	messageLengthInBytes := make([]byte, numBytesForMessageLength)
	reader.Read(messageLengthInBytes)
	messageLength := binary.LittleEndian.Uint32(messageLengthInBytes)

	messageContentsInBytes := make([]byte, messageLength)
	_, err := reader.Read(messageContentsInBytes)
	log.Debugf("Message declared its payload size as [%d] bytes", messageLength)
	return messageContentsInBytes, err
}

func checkIfResponseHasConduitError(rsp *http.Response) error {
	errorMsg := rsp.Header.Get(errorHeader)

	if errorMsg != "" {
		reader := bufio.NewReader(rsp.Body)
		defer rsp.Body.Close()
		var apiError pb.ApiError

		err := fromByteStreamToProtocolBuffers(reader, &apiError)
		if err != nil {
			return fmt.Errorf("response is Conduit error header [%s], but response body didn't contain protobuf error: %v", errorMsg, err)
		}

		return errors.New(apiError.Error)
	}

	return nil
}
