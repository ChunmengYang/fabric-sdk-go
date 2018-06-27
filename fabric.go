package fabric

import (
	"fmt"
	"path"
	"strconv"
	"time"
	"github.com/Sirupsen/logrus"

	"github.com/hyperledger/fabric-sdk-go/pkg/common/errors/retry"
	"github.com/hyperledger/fabric-sdk-go/pkg/common/providers/msp"
	"github.com/hyperledger/fabric-sdk-go/pkg/client/channel"
	"github.com/hyperledger/fabric-sdk-go/pkg/client/resmgmt"
	"github.com/hyperledger/fabric-sdk-go/pkg/core/config"

	mspclient "github.com/hyperledger/fabric-sdk-go/pkg/client/msp"

	"github.com/hyperledger/fabric-sdk-go/pkg/common/providers/core"
	packager "github.com/hyperledger/fabric-sdk-go/pkg/fab/ccpackager/gopackager"
	"github.com/hyperledger/fabric-sdk-go/pkg/fabsdk"
	"github.com/hyperledger/fabric-sdk-go/third_party/github.com/hyperledger/fabric/common/cauthdsl"
)

const (
	channelID      = "orgchannel"
	orgName        = "org1"
	orgAdmin       = "Admin"
	ordererOrgName = "ordererorg"
	ccID           = "exampleCC"
)

var (
	ccInitArgs = [][]byte{[]byte("init"), []byte("a"), []byte("100"), []byte("b"), []byte("200")}
	ccUpgradeArgs = [][]byte{[]byte("init"), []byte("a"), []byte("100"), []byte("b"), []byte("400")}
	ccQueryArgs = [][]byte{[]byte("query"), []byte("b")}
	ccTxArgs =[][]byte{[]byte("move"), []byte("a"), []byte("b"), []byte("1")}
)

func Run() {
	setupAndRun(false, config.FromFile("./config/config.yaml"))
}

func RunWithoutSetup() {
	setupAndRun(true, config.FromFile("./config/config.yaml"))
}

// setupAndRun enables testing an end-to-end scenario against the supplied SDK options
// the doSetup flag will be used to either create a channel and the example CC or not(ie run the tests with existing ch and CC)
func setupAndRun(doSetup bool, configOpt core.ConfigProvider, sdkOpts ...fabsdk.Option) {
	sdk, err := fabsdk.New(configOpt, sdkOpts...)
	if err != nil {
		logrus.Fatalln(fmt.Sprintf("Failed to create new SDK: %s", err))
	}
	defer sdk.Close()

	if doSetup {
		createChannelAndCC(sdk)
	}

	//prepare channel client context using client context
	clientChannelContext := sdk.ChannelContext(channelID, fabsdk.WithUser("User1"), fabsdk.WithOrg(orgName))
	// Channel client is used to query and execute transactions (Org1 is default org)
	client, err := channel.New(clientChannelContext)
	if err != nil {
		logrus.Fatalln(fmt.Sprintf("Failed to create new channel client: %s", err))
	}

	value := queryCC(client)

	eventID := "mash([a-zA-Z]+)"

	// Register chaincode event (pass in channel which receives event details when the event is complete)
	reg, notifier, err := client.RegisterChaincodeEvent(ccID, eventID)
	if err != nil {
		logrus.Fatalln(fmt.Sprintf("Failed to register cc event: %s", err))
	}
	defer client.UnregisterChaincodeEvent(reg)

	// Move funds
	executeCC(client)

	select {
	case ccEvent := <-notifier:
		logrus.Info(fmt.Sprintf("Received CC event: %#v\n", ccEvent))
	case <-time.After(time.Second * 20):
		logrus.Fatalln(fmt.Sprintf("Did NOT receive CC event for eventId(%s)\n", eventID))
	}

	// Verify move funds transaction result
	verifyFundsIsMoved(client, value)

}

func createChannelAndCC(sdk *fabsdk.FabricSDK) {
	//clientContext allows creation of transactions using the supplied identity as the credential.
	clientContext := sdk.Context(fabsdk.WithUser(orgAdmin), fabsdk.WithOrg(ordererOrgName))

	// Resource management client is responsible for managing channels (create/update channel)
	// Supply user that has privileges to create channel (in this case orderer admin)
	resMgmtClient, err := resmgmt.New(clientContext)
	if err != nil {
		logrus.Fatalln(fmt.Sprintf("Failed to create channel management client: %s", err))
	}

	// Create channel

	// Org admin user is signing user for creating channel

	createChannel(sdk, resMgmtClient)

	//prepare context
	adminContext := sdk.Context(fabsdk.WithUser(orgAdmin), fabsdk.WithOrg(orgName))

	// Org resource management client
	orgResMgmt, err := resmgmt.New(adminContext)
	if err != nil {
		logrus.Fatalln(fmt.Sprintf("Failed to create new resource management client: %s", err))
	}

	// Org peers join channel
	if err = orgResMgmt.JoinChannel(channelID, resmgmt.WithRetry(retry.DefaultResMgmtOpts), resmgmt.WithOrdererEndpoint("orderer.example.com")); err != nil {
		logrus.Fatalln(fmt.Sprintf("Org peers failed to JoinChannel: %s", err))
	}

	// Create chaincode package for example cc
	createCC(orgResMgmt)
}

func verifyFundsIsMoved(client *channel.Client, value []byte) {
	newValue := queryCC(client)
	valueInt, err := strconv.Atoi(string(value))
	if err != nil {
		logrus.Fatalln(err.Error())
	}
	valueAfterInvokeInt, err := strconv.Atoi(string(newValue))
	if err != nil {
		logrus.Fatalln(err.Error())
	}
	if valueInt+1 != valueAfterInvokeInt {
		logrus.Fatalln(fmt.Sprintf("Execute failed. Before: %s, after: %s", value, newValue))
	}
}

func executeCC(client *channel.Client) {
	_, err := client.Execute(channel.Request{ChaincodeID: ccID, Fcn: "invoke", Args: ccTxArgs},
		channel.WithRetry(retry.DefaultChannelOpts))
	if err != nil {
		logrus.Fatalln(fmt.Sprintf("Failed to move funds: %s", err))
	}
}

func queryCC(client *channel.Client) []byte {
	response, err := client.Query(channel.Request{ChaincodeID: ccID, Fcn: "invoke", Args: ccQueryArgs},
		channel.WithRetry(retry.DefaultChannelOpts))
	if err != nil {
		logrus.Fatalln(fmt.Sprintf("Failed to query funds: %s", err))
	}
	return response.Payload
}



func createCC(orgResMgmt *resmgmt.Client) {
	ccPkg, err := packager.NewCCPackage("example_cc", "./chaincode")
	if err != nil {
		logrus.Fatalln(err.Error())
	}
	// Install example cc to org peers
	installCCReq := resmgmt.InstallCCRequest{
		Name: ccID,
		Path: "example_cc",
		Version: "0",
		Package: ccPkg,
	}
	_, err = orgResMgmt.InstallCC(installCCReq, resmgmt.WithRetry(retry.DefaultResMgmtOpts))
	if err != nil {
		logrus.Fatalln(err.Error())
	}


	// Set up chaincode policy
	ccPolicy := cauthdsl.SignedByAnyMember([]string{"Org1MSP"})
	// Org resource manager will instantiate 'example_cc' on channel
	resp, err := orgResMgmt.InstantiateCC(
		channelID,
		resmgmt.InstantiateCCRequest{
			Name: ccID,
			Path: "example_cc",
			Version: "0",
			Args: ccInitArgs,
			Policy: ccPolicy,
		},
		resmgmt.WithRetry(retry.DefaultResMgmtOpts),
	)

	if err != nil {
		logrus.Fatalln(fmt.Sprintf("Failed to instantiate chaincode: %s", err))
		return
	}
	logrus.Info(fmt.Sprintf("Transaction ID: %s", resp))
}

func upgradeCC(org1ResMgmt, org2ResMgmt *resmgmt.Client) {
	ccPkg, err := packager.NewCCPackage("example_cc", "./chaincode")
	if err != nil {
		logrus.Fatalln(err.Error())
	}

	installCCReq := resmgmt.InstallCCRequest{Name: ccID, Path: "example_cc", Version: "1", Package: ccPkg}
	// Install example cc version '1' to Org1 peers
	_, err = org1ResMgmt.InstallCC(installCCReq, resmgmt.WithRetry(retry.DefaultResMgmtOpts))
	if err != nil {
		logrus.Fatalln(err.Error())
	}
	// Install example cc version '1' to Org2 peers
	_, err = org2ResMgmt.InstallCC(installCCReq, resmgmt.WithRetry(retry.DefaultResMgmtOpts))
	if err != nil {
		logrus.Fatalln(err.Error())
	}
	// New chaincode policy (both orgs have to approve)
	ccPolicy, err := cauthdsl.FromString("AND ('Org1MSP.member','Org2MSP.member')")
	if err != nil {
		logrus.Fatalln(err.Error())
	}
	// Org1 resource manager will instantiate 'example_cc' version 1 on 'orgchannel'
	upgradeResp, err := org1ResMgmt.UpgradeCC(channelID, resmgmt.UpgradeCCRequest{Name: ccID, Path: "example_cc", Version: "1", Args: ccUpgradeArgs, Policy: ccPolicy})
	if err != nil {
		logrus.Fatalln(fmt.Sprintf("Failed to upgrade chaincode: %s", err))
		return
	}
	logrus.Info(fmt.Sprintf("Transaction ID: %s", upgradeResp))
}

func createChannel(sdk *fabsdk.FabricSDK, resMgmtClient *resmgmt.Client) {
	mspClient, err := mspclient.New(sdk.Context(), mspclient.WithOrg(orgName))
	if err != nil {
		logrus.Fatalln(err.Error())
	}
	adminIdentity, err := mspClient.GetSigningIdentity(orgAdmin)
	if err != nil {
		logrus.Fatalln(err.Error())
	}
	req := resmgmt.SaveChannelRequest{ChannelID: channelID,
		ChannelConfigPath: path.Join("./channel", "orgchannel.tx"),
		SigningIdentities: []msp.SigningIdentity{adminIdentity}}
	txID, err := resMgmtClient.SaveChannel(req, resmgmt.WithRetry(retry.DefaultResMgmtOpts), resmgmt.WithOrdererEndpoint("orderer.example.com"))

	if err != nil {
		logrus.Fatalln(fmt.Sprintf("Failed to create channel: %s", err))
		return
	}
	logrus.Info(fmt.Sprintf("Transaction ID: %s", txID))
}
