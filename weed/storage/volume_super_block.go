package storage

import (
	"fmt"
	"os"

	"github.com/chrislusf/seaweedfs/weed/glog"
	"github.com/chrislusf/seaweedfs/weed/pb/master_pb"
	"github.com/chrislusf/seaweedfs/weed/storage/backend"
	"github.com/chrislusf/seaweedfs/weed/storage/needle"
	"github.com/chrislusf/seaweedfs/weed/util"
	"github.com/golang/protobuf/proto"
)

const (
	_SuperBlockSize = 8
)

/*
* Super block currently has 8 bytes allocated for each volume.
* Byte 0: version, 1 or 2
* Byte 1: Replica Placement strategy, 000, 001, 002, 010, etc
* Byte 2 and byte 3: Time to live. See TTL for definition
* Byte 4 and byte 5: The number of times the volume has been compacted.
* Rest bytes: Reserved
 */
type SuperBlock struct {
	version            needle.Version
	ReplicaPlacement   *ReplicaPlacement
	Ttl                *needle.TTL
	CompactionRevision uint16
	Extra              *master_pb.SuperBlockExtra
	extraSize          uint16
}

func (s *SuperBlock) BlockSize() int {
	switch s.version {
	case needle.Version2, needle.Version3:
		return _SuperBlockSize + int(s.extraSize)
	}
	return _SuperBlockSize
}

func (s *SuperBlock) Version() needle.Version {
	return s.version
}
func (s *SuperBlock) Bytes() []byte {
	header := make([]byte, _SuperBlockSize)
	header[0] = byte(s.version)
	header[1] = s.ReplicaPlacement.Byte()
	s.Ttl.ToBytes(header[2:4])
	util.Uint16toBytes(header[4:6], s.CompactionRevision)

	if s.Extra != nil {
		extraData, err := proto.Marshal(s.Extra)
		if err != nil {
			glog.Fatalf("cannot marshal super block extra %+v: %v", s.Extra, err)
		}
		extraSize := len(extraData)
		if extraSize > 256*256-2 {
			// reserve a couple of bits for future extension
			glog.Fatalf("super block extra size is %d bigger than %d", extraSize, 256*256-2)
		}
		s.extraSize = uint16(extraSize)
		util.Uint16toBytes(header[6:8], s.extraSize)

		header = append(header, extraData...)
	}

	return header
}

func (s *SuperBlock) Initialized() bool {
	return s.ReplicaPlacement != nil && s.Ttl != nil
}

func (v *Volume) maybeWriteSuperBlock() error {

	datSize, _, e := v.DataBackend.GetStat()
	if e != nil {
		glog.V(0).Infof("failed to stat datafile %s: %v", v.DataBackend.String(), e)
		return e
	}
	if datSize == 0 {
		v.SuperBlock.version = needle.CurrentVersion
		_, e = v.DataBackend.WriteAt(v.SuperBlock.Bytes(), 0)
		if e != nil && os.IsPermission(e) {
			//read-only, but zero length - recreate it!
			var dataFile *os.File
			if dataFile, e = os.Create(v.DataBackend.String()); e == nil {
				v.DataBackend = backend.NewDiskFile(dataFile)
				if _, e = v.DataBackend.WriteAt(v.SuperBlock.Bytes(), 0); e == nil {
					v.readOnly = false
				}
			}
		}
	}
	return e
}

func (v *Volume) readSuperBlock() (err error) {
	v.SuperBlock, err = ReadSuperBlock(v.DataBackend)
	return err
}

// ReadSuperBlock reads from data file and load it into volume's super block
func ReadSuperBlock(datBackend backend.BackendStorageFile) (superBlock SuperBlock, err error) {

	header := make([]byte, _SuperBlockSize)
	if _, e := datBackend.ReadAt(header, 0); e != nil {
		err = fmt.Errorf("cannot read volume %s super block: %v", datBackend.String(), e)
		return
	}

	superBlock.version = needle.Version(header[0])
	if superBlock.ReplicaPlacement, err = NewReplicaPlacementFromByte(header[1]); err != nil {
		err = fmt.Errorf("cannot read replica type: %s", err.Error())
		return
	}
	superBlock.Ttl = needle.LoadTTLFromBytes(header[2:4])
	superBlock.CompactionRevision = util.BytesToUint16(header[4:6])
	superBlock.extraSize = util.BytesToUint16(header[6:8])

	if superBlock.extraSize > 0 {
		// read more
		extraData := make([]byte, int(superBlock.extraSize))
		superBlock.Extra = &master_pb.SuperBlockExtra{}
		err = proto.Unmarshal(extraData, superBlock.Extra)
		if err != nil {
			err = fmt.Errorf("cannot read volume %s super block extra: %v", datBackend.String(), err)
			return
		}
	}

	return
}
