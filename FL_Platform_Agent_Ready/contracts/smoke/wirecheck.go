// Standalone Go contract oracle; copied into a disposable generated module.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	pb "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/gen/go/flplatform/v1"
	"google.golang.org/protobuf/proto"
)

const maxBytes uint64 = 256 * 1024 * 1024

func canonical(s *pb.ModelSchema) (string, uint64, error) {
	if s.GetSchemaVersion() != 1 || len(s.GetTensors()) == 0 || len(s.Tensors) > 4096 {
		return "", 0, fmt.Errorf("schema version/count")
	}
	var b strings.Builder
	b.WriteString("FLMODEL1\n")
	names := map[string]bool{}
	var total uint64
	pattern := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]{0,127}$`)
	for _, t := range s.Tensors {
		if t == nil || !pattern.MatchString(t.Name) || names[t.Name] || t.Dtype != "FLOAT32_LE" || len(t.Shape) > 8 {
			return "", 0, fmt.Errorf("tensor metadata")
		}
		names[t.Name] = true
		n := uint64(1)
		dims := []string{}
		for _, d := range t.Shape {
			if d == 0 || d > 2147483647 || n > maxBytes/4/d {
				return "", 0, fmt.Errorf("dimension/overflow")
			}
			n *= d
			dims = append(dims, strconv.FormatUint(d, 10))
		}
		total += n
		if n != t.Numel || total > maxBytes/4 {
			return "", 0, fmt.Errorf("numel/limit")
		}
		shape := strings.Join(dims, ",")
		if len(dims) == 0 {
			shape = "-"
		}
		fmt.Fprintf(&b, "%s\tFLOAT32_LE\t%s\t%d\n", t.Name, shape, n)
	}
	return b.String(), total * 4, nil
}

func validate(s *pb.ModelSchema, chunks []*pb.ArtifactChunk) error {
	text, size, err := canonical(s)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return fmt.Errorf("empty stream")
	}
	schemaSHA := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	first := chunks[0]
	if first == nil {
		return fmt.Errorf("nil chunk")
	}
	var offset uint64
	h := sha256.New()
	for _, c := range chunks {
		if c == nil || c.UploadId != first.UploadId || c.ExpectedSha256Hex != first.ExpectedSha256Hex ||
			c.TaskId != first.TaskId || c.LeaseId != first.LeaseId || c.LeaseToken != first.LeaseToken {
			return fmt.Errorf("changed metadata")
		}
		if c.Codec != "FLOAT32_LE" || c.ModelSchemaHash != schemaSHA || c.TotalSizeBytes != size ||
			c.Offset != offset || len(c.Data) == 0 || len(c.Data) > 1024*1024 || len(c.Data)%4 != 0 ||
			uint64(len(c.Data)) > size-offset {
			return fmt.Errorf("chunk boundary/schema")
		}
		for i := 0; i < len(c.Data); i += 4 {
			v := float64(math.Float32frombits(binary.LittleEndian.Uint32(c.Data[i : i+4])))
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return fmt.Errorf("non-finite")
			}
		}
		_, _ = h.Write(c.Data)
		offset += uint64(len(c.Data))
	}
	if offset != size || hex.EncodeToString(h.Sum(nil)) != first.ExpectedSha256Hex {
		return fmt.Errorf("truncated/checksum")
	}
	return nil
}

func read(path string, message proto.Message) {
	data, err := os.ReadFile(path)
	must(err)
	must(proto.Unmarshal(data, message))
}

func write(path string, message proto.Message) {
	data, err := proto.Marshal(message)
	must(err)
	must(os.WriteFile(path, data, 0600))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	s, c, assignment := &pb.ModelSchema{}, &pb.ArtifactChunk{}, &pb.TaskAssignment{}
	read("schema.bin", s)
	read("python.bin", c)
	read("assignment.bin", assignment)
	if assignment.Attempt != 1 || !proto.Equal(assignment.ModelSchema, s) || assignment.GlobalModel.SchemaVersion != 1 {
		panic("assignment mismatch")
	}
	text, _, err := canonical(s)
	must(err)
	expected, err := os.ReadFile("schema.sha")
	must(err)
	if fmt.Sprintf("%x", sha256.Sum256([]byte(text))) != string(expected) {
		panic("cross-language hash")
	}
	must(validate(s, []*pb.ArtifactChunk{c}))
	want := []float32{1, -2.5, 0, 3.25}
	for i, v := range want {
		if math.Float32frombits(binary.LittleEndian.Uint32(c.Data[i*4:i*4+4])) != v {
			panic("weight value")
		}
	}
	changes := []func(*pb.ArtifactChunk){
		func(x *pb.ArtifactChunk) { x.Offset = 4 },
		func(x *pb.ArtifactChunk) { x.Codec = "FLOAT32_BE" },
		func(x *pb.ArtifactChunk) { x.Data = x.Data[:len(x.Data)-4] },
		func(x *pb.ArtifactChunk) { x.ModelSchemaHash = strings.Repeat("0", 64) },
		func(x *pb.ArtifactChunk) { x.TotalSizeBytes = maxBytes + 4 },
		func(x *pb.ArtifactChunk) { x.ExpectedSha256Hex = strings.Repeat("0", 64) },
		func(x *pb.ArtifactChunk) {
			binary.LittleEndian.PutUint32(x.Data[:4], math.Float32bits(float32(math.NaN())))
			x.ExpectedSha256Hex = fmt.Sprintf("%x", sha256.Sum256(x.Data))
		},
	}
	for _, change := range changes {
		broken := proto.Clone(c).(*pb.ArtifactChunk)
		change(broken)
		if validate(s, []*pb.ArtifactChunk{broken}) == nil {
			panic("invalid chunk accepted")
		}
	}
	for _, mutate := range []func(*pb.ModelSchema){
		func(x *pb.ModelSchema) { x.Tensors[0].Shape[0] = 0 },
		func(x *pb.ModelSchema) { x.Tensors[0].Shape = []uint64{2147483647, 2147483647} },
		func(x *pb.ModelSchema) { x.Tensors[1].Name = x.Tensors[0].Name },
		func(x *pb.ModelSchema) { x.Tensors[0], x.Tensors[1] = x.Tensors[1], x.Tensors[0] },
	} {
		broken := proto.Clone(s).(*pb.ModelSchema)
		mutate(broken)
		if validate(broken, []*pb.ArtifactChunk{c}) == nil {
			panic("invalid schema accepted")
		}
	}
	first, last := proto.Clone(c).(*pb.ArtifactChunk), proto.Clone(c).(*pb.ArtifactChunk)
	first.Data, last.Data, last.Offset = c.Data[:8], c.Data[8:], 8
	must(validate(s, []*pb.ArtifactChunk{first, last}))
	if validate(s, []*pb.ArtifactChunk{last, first}) == nil {
		panic("reordered chunks accepted")
	}
	c.UploadId, c.TaskId, c.LeaseId, c.LeaseToken = "", "", "", ""
	write("go.bin", c)
	write("response.bin", &pb.SubmitUpdateResponse{Accepted: true, UpdateId: "from-go", StatusCode: "ACCEPTED"})
	fmt.Println("PASS: Python -> Go values/hash, 12 negative cases, Go response generated")
}
