// +build !libstorage_storage_driver libstorage_storage_driver_rbd

package rbd

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	log "github.com/Sirupsen/logrus"
	gofig "github.com/akutz/gofig/types"
	"github.com/stretchr/testify/assert"

	"github.com/codedellemc/libstorage/api/server"
	apitests "github.com/codedellemc/libstorage/api/tests"
	"github.com/codedellemc/libstorage/api/types"

	// load the  driver
	"github.com/codedellemc/libstorage/drivers/storage/rbd"
	rbdx "github.com/codedellemc/libstorage/drivers/storage/rbd/executor"
	rbdu "github.com/codedellemc/libstorage/drivers/storage/rbd/utils"
)

var (
	defaultPool = "rbd"
	testPool    = "test"
	configYAML  = []byte(`
rbd:
  defaultPool: rbd
`)

	volumeName  string
	volumeName2 string
)

func skipTests() bool {
	travis, _ := strconv.ParseBool(os.Getenv("TRAVIS"))
	noTest, _ := strconv.ParseBool(os.Getenv("TEST_SKIP_RBD"))
	return travis || noTest
}

func init() {
	uuid, _ := types.NewUUID()
	uuids := strings.Split(uuid.String(), "-")
	volumeName = uuids[0]
	volumeName2 = uuids[1] + "-test"
}

func TestMain(m *testing.M) {
	server.CloseOnAbort()
	ec := m.Run()
	os.Exit(ec)
}

// Test that the default behavior just works -- reads a real config file
// and the real IP/route information from machine under test
func TestInstanceID(t *testing.T) {
	if skipTests() {
		t.SkipNow()
	}

	iid, err := rbdx.GetInstanceID(nil, nil, nil)
	assert.NoError(t, err)
	if err != nil {
		t.Error("failed TestInstanceID")
		t.FailNow()
	}

	assert.NotEqual(t, iid, "")

	apitests.Run(
		t, rbd.Name, configYAML,
		(&apitests.InstanceIDTest{
			Driver:   rbd.Name,
			Expected: iid,
		}).Test)
}

// Seed the function with known addresses to verify that the right ones
// are selected
type testIPInput struct {
	monIPs     []net.IP
	interfaces []net.Addr
	iid        net.IP
}

type dummyAddr struct {
	ip net.IP
}

func (a *dummyAddr) Network() string {
	return "dummy"
}

func (a *dummyAddr) String() string {
	return fmt.Sprintf("%s/24", a.ip.String())
}

var (
	ipZeroDotTwo    = net.ParseIP("192.168.0.2")
	ipOneDotTwo     = net.ParseIP("192.168.1.2")
	ipZeroDotTen    = net.ParseIP("192.168.0.10")
	ipZeroDotTwenty = net.ParseIP("192.168.0.20")
	ipZeroDotThirty = net.ParseIP("192.168.0.30")
	ipv6            = net.ParseIP("2001:db8:85a3::8a2e:370:7334")
	ipv6Local       = net.ParseIP("::1")
)

var testIPs = []testIPInput{
	// Test direct L2 connection with single monitor
	{
		monIPs:     []net.IP{ipZeroDotTen},
		interfaces: []net.Addr{&dummyAddr{ipZeroDotTwo}},
		iid:        ipZeroDotTwo,
	},
	// Test direct L2 connection with multiple monitors,
	// multiple local networks
	{
		monIPs: []net.IP{
			ipZeroDotTen, ipZeroDotTwenty, ipZeroDotThirty,
		},
		interfaces: []net.Addr{
			&dummyAddr{ipOneDotTwo}, &dummyAddr{ipZeroDotTwo},
		},
		iid: ipZeroDotTwo,
	},
	// Test L3 connection (routing required)
	{
		monIPs: []net.IP{
			ipZeroDotTen, ipZeroDotTwenty, ipZeroDotThirty,
		},
		interfaces: []net.Addr{&dummyAddr{ipOneDotTwo}},
		iid:        nil,
	},
}

func TestInstanceIDSimulatedIPs(t *testing.T) {
	if skipTests() {
		t.SkipNow()
	}

	for _, test := range testIPs {
		iid, err := rbdx.GetInstanceID(
			nil, test.monIPs, test.interfaces)

		assert.NoError(t, err)
		if err != nil {
			t.Error("failed TestInstanceIDSimulatedIPs")
			t.FailNow()
		}
		assert.NotEqual(t, iid, "")
		if test.iid != nil {
			assert.True(t, test.iid.Equal(net.ParseIP(iid.ID)))
		}
	}
}

type testAddrInput struct {
	address []string
	ip      net.IP
}

var testAddrs = []testAddrInput{
	{
		address: []string{"192.168.0.2"},
		ip:      ipZeroDotTwo,
	},
	{
		address: []string{"192.168.0.2:6789"},
		ip:      ipZeroDotTwo,
	},
	{
		address: []string{"[2001:db8:85a3::8a2e:370:7334]"},
		ip:      ipv6,
	},
	{
		address: []string{"[2001:db8:85a3::8a2e:370:7334]:6789"},
		ip:      ipv6,
	},
	{
		address: []string{"[::1]"},
		ip:      ipv6Local,
	},
}

func TestParseMonitorAddresses(t *testing.T) {
	if skipTests() {
		t.SkipNow()
	}

	for _, test := range testAddrs {
		ip, err := rbdu.ParseMonitorAddresses(test.address)

		assert.NoError(t, err, "failed with %s", test.address[0])
		if err != nil {
			t.Error("failed TestParseMonitorAddresses")
			t.FailNow()
		}
		assert.True(t, test.ip.Equal(ip[0]))
	}
}

func TestServices(t *testing.T) {
	if skipTests() {
		t.SkipNow()
	}

	tf := func(config gofig.Config, client types.Client, t *testing.T) {
		reply, err := client.API().Services(nil)
		assert.NoError(t, err)
		assert.Equal(t, len(reply), 1)

		_, ok := reply[rbd.Name]
		assert.True(t, ok)
	}
	apitests.Run(t, rbd.Name, configYAML, tf)
}

func volumeCreate(
	t *testing.T,
	client types.Client,
	volumeName string,
	pool *string) *types.Volume {

	log.WithField("volumeName", volumeName).Info("creating volume")
	size := int64(8)

	opts := map[string]interface{}{
		"priority": 2,
		"owner":    "root@example.com",
	}

	createName := volumeName
	volID := fmt.Sprintf("%s.%s", defaultPool, volumeName)
	expectedPool := defaultPool
	if pool != nil {
		createName = fmt.Sprintf("%s.%s", *pool, volumeName)
		volID = fmt.Sprintf("%s.%s", *pool, volumeName)
		expectedPool = *pool
	}

	volumeCreateRequest := &types.VolumeCreateRequest{
		Name: createName,
		Size: &size,
		Opts: opts,
	}

	reply, err := client.API().VolumeCreate(nil, rbd.Name, volumeCreateRequest)
	assert.NoError(t, err)
	if err != nil {
		t.FailNow()
		t.Error("failed volumeCreate")
	}
	apitests.LogAsJSON(reply, t)

	assert.Equal(t, volumeName, reply.Name)
	assert.Equal(t, size, reply.Size)
	assert.Equal(t, expectedPool, reply.Type)
	assert.Equal(t, volID, reply.ID)
	return reply
}

func volumeRemove(t *testing.T, client types.Client, volumeID string) {
	log.WithField("volumeID", volumeID).Info("removing volume")
	err := client.API().VolumeRemove(
		nil, rbd.Name, volumeID, false)
	assert.NoError(t, err)
	if err != nil {
		t.Error("failed volumeRemove")
		t.FailNow()
	}
}

func TestVolumeCreateRemove(t *testing.T) {
	if skipTests() {
		t.SkipNow()
	}

	tf := func(config gofig.Config, client types.Client, t *testing.T) {
		// Use "name only" syntax
		vol := volumeCreate(t, client, volumeName, nil)
		volumeRemove(t, client, vol.ID)

		// Specify exact pool, even though it is default
		vol = volumeCreate(t, client, volumeName, &defaultPool)
		volumeRemove(t, client, vol.ID)

		// Make sure using a non-default pool works
		vol = volumeCreate(t, client, volumeName, &testPool)
		volumeRemove(t, client, vol.ID)
	}
	apitests.Run(t, rbd.Name, configYAML, tf)
}

func volumeByName(
	t *testing.T,
	client types.Client,
	volumeName string,
	pool *string) *types.Volume {

	log.WithField("volumeName", volumeName).Info("get volume name")
	vols, err := client.API().Volumes(nil, 0)
	assert.NoError(t, err)
	if err != nil {
		t.FailNow()
	}
	volID := fmt.Sprintf("%s.%s", *pool, volumeName)
	assert.Contains(t, vols, rbd.Name)
	for _, vol := range vols[rbd.Name] {
		if vol.Name == volumeName && vol.ID == volID {
			return vol
		}
	}
	t.FailNow()
	t.Error("failed volumeByName")
	return nil
}

func TestVolumes(t *testing.T) {
	if skipTests() {
		t.SkipNow()
	}

	tf := func(config gofig.Config, client types.Client, t *testing.T) {
		_ = volumeCreate(t, client, volumeName, nil)
		_ = volumeCreate(t, client, volumeName2, &testPool)

		vol1 := volumeByName(t, client, volumeName, &defaultPool)
		vol2 := volumeByName(t, client, volumeName2, &testPool)

		volumeRemove(t, client, vol1.ID)
		volumeRemove(t, client, vol2.ID)
	}
	apitests.Run(t, rbd.Name, configYAML, tf)
}

func volumeAttach(
	t *testing.T, client types.Client, volumeID string) *types.Volume {

	log.WithField("volumeID", volumeID).Info("attaching volume")
	reply, token, err := client.API().VolumeAttach(
		nil, rbd.Name, volumeID, &types.VolumeAttachRequest{})

	assert.NoError(t, err)
	if err != nil {
		t.Error("failed volumeAttach")
		t.FailNow()
	}
	apitests.LogAsJSON(reply, t)
	assert.NotEqual(t, token, "")

	return reply
}

func volumeDetach(
	t *testing.T, client types.Client, volumeID string) *types.Volume {

	log.WithField("volumeID", volumeID).Info("detaching volume")
	reply, err := client.API().VolumeDetach(
		nil, rbd.Name, volumeID, &types.VolumeDetachRequest{})
	assert.NoError(t, err)
	if err != nil {
		t.Error("failed volumeDetach")
		t.FailNow()
	}
	apitests.LogAsJSON(reply, t)
	assert.Len(t, reply.Attachments, 0)
	return reply
}

func volumeInspect(
	t *testing.T,
	client types.Client,
	volumeID string,
	attachFlag types.VolumeAttachmentsTypes) *types.Volume {

	log.WithField("volumeID", volumeID).Info("inspecting volume")
	reply, err := client.API().VolumeInspect(
		nil, rbd.Name, volumeID, attachFlag)
	assert.NoError(t, err)

	if err != nil {
		t.Error("failed volumeInspect")
		t.FailNow()
	}
	apitests.LogAsJSON(reply, t)
	return reply
}

func volumeInspectAttached(
	t *testing.T, client types.Client, volumeID string) *types.Volume {

	reply := volumeInspect(t, client, volumeID, types.VolAttReqForInstance)
	assert.Len(t, reply.Attachments, 1)
	assert.Equal(t, "", reply.Attachments[0].DeviceName)
	return reply
}

func volumeInspectNoAttachments(
	t *testing.T, client types.Client, volumeID string) *types.Volume {

	reply := volumeInspect(t, client, volumeID, types.VolAttFalse)
	assert.Len(t, reply.Attachments, 0)
	return reply
}

func volumeInspectAttachedDevices(
	t *testing.T, client types.Client, volumeID string) *types.Volume {

	reply := volumeInspect(t, client, volumeID,
		types.VolAttReqWithDevMapForInstance)
	assert.Len(t, reply.Attachments, 1)
	assert.NotEqual(t, "", reply.Attachments[0].DeviceName)
	return reply
}

func volumeInspectDetached(
	t *testing.T, client types.Client, volumeID string) *types.Volume {

	reply := volumeInspect(t, client, volumeID, types.VolAttReq)
	assert.Len(t, reply.Attachments, 0)
	return reply
}

func volumeInspectAvailable(
	t *testing.T, client types.Client, volumeID string) *types.Volume {

	reply := volumeInspect(t, client, volumeID,
		types.VolAttReqOnlyUnattachedVols)
	assert.Len(t, reply.Attachments, 0)
	return reply
}

func volumeAttachFail(
	t *testing.T, client types.Client, volumeID string) *types.Volume {

	log.WithField("volumeID", volumeID).Info("attaching volume")
	reply, _, err := client.API().VolumeAttach(
		nil, rbd.Name, volumeID, &types.VolumeAttachRequest{})

	assert.Error(t, err)
	if err == nil {
		t.Error("volumeAttach succeeded when it should have failed")
		t.FailNow()
	}
	apitests.LogAsJSON(reply, t)

	return reply
}

func TestVolumeAttach(t *testing.T) {
	if skipTests() {
		t.SkipNow()
	}
	var vol *types.Volume
	tf := func(config gofig.Config, client types.Client, t *testing.T) {
		vol = volumeCreate(t, client, volumeName, nil)
		_ = volumeAttach(t, client, vol.ID)
		_ = volumeInspectAttached(t, client, vol.ID)
		_ = volumeInspectAttachedDevices(t, client, vol.ID)
		_ = volumeInspectNoAttachments(t, client, vol.ID)
		_ = volumeAttachFail(t, client, vol.ID)
		_ = volumeDetach(t, client, vol.ID)
		_ = volumeInspectDetached(t, client, vol.ID)
		_ = volumeInspectAvailable(t, client, vol.ID)
		volumeRemove(t, client, vol.ID)
	}
	apitests.Run(t, rbd.Name, configYAML, tf)
}
