package morph

import (
	"fmt"

	"github.com/nspcc-dev/neo-go/pkg/io"
	"github.com/nspcc-dev/neo-go/pkg/smartcontract/callflag"
	"github.com/nspcc-dev/neo-go/pkg/util"
	"github.com/nspcc-dev/neo-go/pkg/vm/emit"
	"github.com/nspcc-dev/neo-go/pkg/vm/opcode"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func updateContracts(cmd *cobra.Command, _ []string) error {
	wCtx, err := newInitializeContext(cmd, viper.GetViper())
	if err != nil {
		return fmt.Errorf("initialization error: %w", err)
	}

	if err := wCtx.deployNNS(updateMethodName); err != nil {
		return err
	}

	return wCtx.updateContracts()
}

func hardcodeNNS(cmd *cobra.Command, _ []string) error {
	c, err := newInitializeContext(cmd, viper.GetViper())
	if err != nil {
		return fmt.Errorf("initialization error: %w", err)
	}

	nnsCs, err := c.Client.GetContractStateByID(1)
	if err != nil {
		panic(err)
	}

	/* FIX for mainnet, set hardcoded hashes. */
	hashes := map[string]string{
		//"neofs.neofs":      "2cafa46838e8b564468ebd868dcafdd99dce6221",
		"audit.neofs":      "85fe181f4aa3cbdc94023d97c69001ece0730398",
		"balance.neofs":    "dc1ec98d9d0c5f9dfade16144defe08cffc5ca55",
		"container.neofs":  "1b6e68d299b570e1cb7e86eadfdc06aa2e8e0cc5",
		"neofsid.neofs":    "0a64ce753653cc97c0467e1334d9d3678ca8c682",
		"netmap.neofs":     "7c5bdb23e36cc7cce95bf42f3ab9e452c2501df1",
		"reputation.neofs": "7ad824fd1eeb1565be2cee3889214b9aa605d2fc",
		/* "az": */ getAlphabetNNSDomain(0): "2392438eb31100857c0f161c66791872b249aa13",
		/* "buky": */ getAlphabetNNSDomain(1): "83ef4226d5d6519ca9c99a5de13b1b5ca223a6ad",
		/* "vedi": */ getAlphabetNNSDomain(2): "6250927beaa9aa5a00171379dcb7187b0c91d17d",
		/* "glagoli": */ getAlphabetNNSDomain(3): "1d6a2519ba41a139b2ced1bfd5013938271a7578",
		/* "dobro": */ getAlphabetNNSDomain(4): "b65fc7a3c31cf57a90d7eb1c0e9909e4ca69133c",
		/* "yest": */ getAlphabetNNSDomain(5): "f95b6ff8cd3b027c9911c18115518ad8c5d2f591",
		/* "zhivete": */ getAlphabetNNSDomain(6): "5b17c579bf56884fd68af152432b3b5aee7aee76",
	}

	w := io.NewBufBinWriter()
	for name, sh := range hashes {
		h, err := util.Uint160DecodeStringLE(sh)
		if err != nil {
			return fmt.Errorf("invalid hash: %s", sh)
		}
		cs, err := c.Client.GetContractStateByHash(h)
		if err != nil || cs.Hash != h {
			return fmt.Errorf("contract %s should have hash %s, but: %w", name, sh, err)
		}
		c.Command.Printf("Domain %s will be registered again\n", name)
		emit.AppCall(w.BinWriter, nnsCs.Hash, "register", callflag.All,
			name, c.CommitteeAcc.Contract.ScriptHash(),
			"ops@nspcc.ru", int64(3600), int64(600), int64(604800), int64(3600))
		emit.Opcodes(w.BinWriter, opcode.ASSERT)
		//emit.AppCall(w.BinWriter, nnsCs.Hash, "addRecord", callflag.All,
		//	name, int64(nns.TXT), h.StringLE())
		//c.Command.Printf("NNS: Set %s -> %s\n", name, h.StringLE())
	}
	if w.Err != nil {
		panic(w.Err)
	}

	if err := c.sendCommitteeTx(w.Bytes(), -1, false); err != nil {
		return err
	}
	return c.awaitTx()
}
