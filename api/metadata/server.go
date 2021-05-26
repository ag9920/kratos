package metadata

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io/ioutil"
	"sync"

	"github.com/golang/protobuf/proto"
	dpb "github.com/golang/protobuf/protoc-gen-go/descriptor"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"
)

//go:generate protoc --proto_path=. --proto_path=../../third_party --go_out=paths=source_relative:. --go-grpc_out=paths=source_relative:. --go-http_out=paths=source_relative:. metadata.proto

// Server is api meta server
type Server struct {
	UnimplementedMetadataServer

	srv      *grpc.Server
	lock     sync.Mutex
	services map[string]*descriptorpb.FileDescriptorSet
	methods  map[string][]string
}

// NewServer create server instance
func NewServer(srv *grpc.Server) *Server {
	return &Server{
		srv:      srv,
		services: make(map[string]*descriptorpb.FileDescriptorSet),
		methods:  make(map[string][]string),
	}
}

func (s *Server) load() error {
	if len(s.services) == len(s.srv.GetServiceInfo()) {
		return nil
	}
	for name, info := range s.srv.GetServiceInfo() {
		fdenc, ok := parseMetadata(info.Metadata)
		if !ok {
			return fmt.Errorf("invalid service %s metadata", name)
		}
		fd, err := decodeFileDesc(fdenc)
		if err != nil {
			return err
		}
		protoSet, err := allDependency(fd)
		if err != nil {
			return err
		}
		s.services[name] = &dpb.FileDescriptorSet{File: protoSet}
		for _, method := range info.Methods {
			s.methods[name] = append(s.methods[name], method.Name)
		}
	}
	return nil
}

// ListServices return all services
func (s *Server) ListServices(ctx context.Context, in *ListServicesRequest) (*ListServicesReply, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if err := s.load(); err != nil {
		return nil, err
	}
	reply := new(ListServicesReply)
	for name := range s.services {
		reply.Services = append(reply.Services, name)
	}
	for name, methods := range s.methods {
		for _, method := range methods {
			reply.Methods = append(reply.Methods, fmt.Sprintf("/%s/%s", name, method))
		}
	}
	return reply, nil
}

// GetServiceDesc return service meta by name
func (s *Server) GetServiceDesc(ctx context.Context, in *GetServiceDescRequest) (*GetServiceDescReply, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if err := s.load(); err != nil {
		return nil, err
	}
	fds, ok := s.services[in.Name]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "service %s not found", in.Name)
	}
	return &GetServiceDescReply{FileDescSet: fds}, nil
}

// parseMetadata finds the file descriptor bytes specified meta.
// For SupportPackageIsVersion4, m is the name of the proto file, we
// call proto.FileDescriptor to get the byte slice.
// For SupportPackageIsVersion3, m is a byte slice itself.
func parseMetadata(meta interface{}) ([]byte, bool) {
	// Check if meta is the file name.
	if fileNameForMeta, ok := meta.(string); ok {
		return proto.FileDescriptor(fileNameForMeta), true
	}
	// Check if meta is the byte slice.
	if enc, ok := meta.([]byte); ok {
		return enc, true
	}
	return nil, false
}

// decodeFileDesc does decompression and unmarshalling on the given
// file descriptor byte slice.
func decodeFileDesc(enc []byte) (*dpb.FileDescriptorProto, error) {
	raw, err := decompress(enc)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress enc: %v", err)
	}
	fd := new(dpb.FileDescriptorProto)
	if err := proto.Unmarshal(raw, fd); err != nil {
		return nil, fmt.Errorf("bad descriptor: %v", err)
	}
	return fd, nil
}

func allDependency(fd *dpb.FileDescriptorProto) ([]*dpb.FileDescriptorProto, error) {
	var files []*dpb.FileDescriptorProto
	for _, dep := range fd.Dependency {
		fdDep, err := fileDescriptorProto(dep)
		if err != nil {
			return nil, err
		}
		temp, err := allDependency(fdDep)
		if err != nil {
			return nil, err
		}
		files = append(files, temp...)
	}
	files = append(files, fd)
	return files, nil
}

// decompress does gzip decompression.
func decompress(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("bad gzipped descriptor: %v", err)
	}
	out, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("bad gzipped descriptor: %v", err)
	}
	return out, nil
}

func fileDescriptorProto(path string) (*dpb.FileDescriptorProto, error) {
	fdenc := proto.FileDescriptor(path)
	fdDep, err := decodeFileDesc(fdenc)
	if err != nil {
		return nil, err
	}
	return fdDep, nil
}
