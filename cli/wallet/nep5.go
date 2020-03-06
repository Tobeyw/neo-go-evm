package wallet

import (
	"errors"
	"fmt"

	"github.com/nspcc-dev/neo-go/cli/flags"
	"github.com/nspcc-dev/neo-go/pkg/core"
	"github.com/nspcc-dev/neo-go/pkg/core/transaction"
	"github.com/nspcc-dev/neo-go/pkg/encoding/address"
	"github.com/nspcc-dev/neo-go/pkg/io"
	"github.com/nspcc-dev/neo-go/pkg/rpc/client"
	"github.com/nspcc-dev/neo-go/pkg/rpc/request"
	"github.com/nspcc-dev/neo-go/pkg/util"
	"github.com/nspcc-dev/neo-go/pkg/vm/emit"
	"github.com/nspcc-dev/neo-go/pkg/vm/opcode"
	"github.com/nspcc-dev/neo-go/pkg/wallet"
	"github.com/urfave/cli"
)

func newNEP5Commands() []cli.Command {
	return []cli.Command{
		{
			Name:      "balance",
			Usage:     "get address balance",
			UsageText: "balance --path <path> --rpc <node> --addr <addr> [--token <hash-or-name>]",
			Action:    getNEP5Balance,
			Flags: []cli.Flag{
				walletPathFlag,
				rpcFlag,
				timeoutFlag,
				cli.StringFlag{
					Name:  "addr",
					Usage: "Address to use",
				},
				cli.StringFlag{
					Name:  "token",
					Usage: "Token to use",
				},
			},
		},
		{
			Name:      "import",
			Usage:     "import NEP5 token to a wallet",
			UsageText: "import --path <path> --rpc <node> --token <hash>",
			Action:    importNEP5Token,
			Flags: []cli.Flag{
				walletPathFlag,
				rpcFlag,
				cli.StringFlag{
					Name:  "token",
					Usage: "Token contract hash in LE",
				},
			},
		},
		{
			Name:      "transfer",
			Usage:     "transfer NEP5 tokens",
			UsageText: "transfer --path <path> --rpc <node> --from <addr> --to <addr> --token <hash> --amount string",
			Action:    transferNEP5,
			Flags: []cli.Flag{
				walletPathFlag,
				rpcFlag,
				timeoutFlag,
				fromAddrFlag,
				toAddrFlag,
				cli.StringFlag{
					Name:  "token",
					Usage: "Token to use",
				},
				cli.StringFlag{
					Name:  "amount",
					Usage: "Amount of asset to send",
				},
				cli.StringFlag{
					Name:  "gas",
					Usage: "Amount of GAS to attach to a tx",
				},
			},
		},
	}
}

func getNEP5Balance(ctx *cli.Context) error {
	wall, err := openWallet(ctx.String("path"))
	if err != nil {
		return cli.NewExitError(err, 1)
	}
	defer wall.Close()

	addr := ctx.String("addr")
	addrHash, err := address.StringToUint160(addr)
	if err != nil {
		return cli.NewExitError(fmt.Errorf("invalid address: %v", err), 1)
	}
	acc := wall.GetAccount(addrHash)
	if acc == nil {
		return cli.NewExitError(fmt.Errorf("can't find account for the address: %s", addr), 1)
	}

	gctx, cancel := getGoContext(ctx)
	defer cancel()

	c, err := client.New(gctx, ctx.String("rpc"), client.Options{})
	if err != nil {
		return cli.NewExitError(err, 1)
	}

	var token *wallet.Token
	name := ctx.String("token")
	if name != "" {
		token, err = getMatchingToken(wall, name)
		if err != nil {
			token, err = getMatchingTokenRPC(c, addrHash, name)
			if err != nil {
				return cli.NewExitError(err, 1)
			}
		}
	}

	balances, err := c.GetNEP5Balances(addrHash)
	if err != nil {
		return cli.NewExitError(err, 1)
	}

	for i := range balances.Balances {
		asset := balances.Balances[i].Asset
		if name != "" && !token.Hash.Equals(asset) {
			continue
		}
		fmt.Printf("TokenHash: %s\n", asset)
		fmt.Printf("\tAmount : %s\n", balances.Balances[i].Amount)
		fmt.Printf("\tUpdated: %d\n", balances.Balances[i].LastUpdated)
	}
	return nil
}

func getMatchingToken(w *wallet.Wallet, name string) (*wallet.Token, error) {
	return getMatchingTokenAux(func(i int) *wallet.Token {
		return w.Extra.Tokens[i]
	}, len(w.Extra.Tokens), name)
}

func getMatchingTokenRPC(c *client.Client, addr util.Uint160, name string) (*wallet.Token, error) {
	bs, err := c.GetNEP5Balances(addr)
	if err != nil {
		return nil, err
	}
	get := func(i int) *wallet.Token {
		t, _ := c.NEP5TokenInfo(bs.Balances[i].Asset)
		return t
	}
	return getMatchingTokenAux(get, len(bs.Balances), name)
}

func getMatchingTokenAux(get func(i int) *wallet.Token, n int, name string) (*wallet.Token, error) {
	var token *wallet.Token
	var count int
	for i := 0; i < n; i++ {
		t := get(i)
		if t != nil && (t.Name == name || t.Symbol == name || t.Address == name || t.Hash.StringLE() == name) {
			if count == 1 {
				printTokenInfo(token)
				printTokenInfo(t)
				return nil, errors.New("multiple matching tokens found")
			}
			count++
			token = t
		}
	}
	if count == 0 {
		return nil, errors.New("token was not found")
	}
	return token, nil
}

func importNEP5Token(ctx *cli.Context) error {
	wall, err := openWallet(ctx.String("path"))
	if err != nil {
		return cli.NewExitError(err, 1)
	}
	defer wall.Close()

	tokenHash, err := util.Uint160DecodeStringLE(ctx.String("token"))
	if err != nil {
		return cli.NewExitError(fmt.Errorf("invalid token contract hash: %v", err), 1)
	}

	for _, t := range wall.Extra.Tokens {
		if t.Hash.Equals(tokenHash) {
			printTokenInfo(t)
			return cli.NewExitError("token already exists", 1)
		}
	}

	gctx, cancel := getGoContext(ctx)
	defer cancel()

	c, err := client.New(gctx, ctx.String("rpc"), client.Options{})
	if err != nil {
		return cli.NewExitError(err, 1)
	}

	tok, err := c.NEP5TokenInfo(tokenHash)
	if err != nil {
		return cli.NewExitError(fmt.Errorf("can't receive token info: %v", err), 1)
	}

	wall.AddToken(tok)
	if err := wall.Save(); err != nil {
		return cli.NewExitError(err, 1)
	}
	printTokenInfo(tok)
	return nil
}

func printTokenInfo(tok *wallet.Token) {
	fmt.Printf("Name:\t%s\n", tok.Name)
	fmt.Printf("Symbol:\t%s\n", tok.Symbol)
	fmt.Printf("Hash:\t%s\n", tok.Hash.StringLE())
	fmt.Printf("Decimals: %d\n", tok.Decimals)
	fmt.Printf("Address: %s\n", tok.Address)
}

func transferNEP5(ctx *cli.Context) error {
	wall, err := openWallet(ctx.String("path"))
	if err != nil {
		return cli.NewExitError(err, 1)
	}
	defer wall.Close()

	fromFlag := ctx.Generic("from").(*flags.Address)
	from := fromFlag.Uint160()
	acc := wall.GetAccount(from)
	if acc == nil {
		return cli.NewExitError(fmt.Errorf("can't find account for the address: %s", fromFlag), 1)
	}

	gctx, cancel := getGoContext(ctx)
	defer cancel()
	c, err := client.New(gctx, ctx.String("rpc"), client.Options{})
	if err != nil {
		return cli.NewExitError(err, 1)
	}

	toFlag := ctx.Generic("to").(*flags.Address)
	to := toFlag.Uint160()
	token, err := getMatchingToken(wall, ctx.String("token"))
	if err != nil {
		fmt.Println("Can't find matching token in the wallet. Querying RPC-node for balances.")
		token, err = getMatchingTokenRPC(c, from, ctx.String("token"))
		if err != nil {
			return cli.NewExitError(err, 1)
		}
	}

	amount, err := util.FixedNFromString(ctx.String("amount"), int(token.Decimals))
	if err != nil {
		return cli.NewExitError(fmt.Errorf("invalid amount: %v", err), 1)
	}

	// Note: we don't use invoke function here because it requires
	// 2 round trips instead of one.
	w := io.NewBufBinWriter()
	emit.Int(w.BinWriter, amount)
	emit.Bytes(w.BinWriter, to.BytesBE())
	emit.Bytes(w.BinWriter, from.BytesBE())
	emit.Int(w.BinWriter, 3)
	emit.Opcode(w.BinWriter, opcode.PACK)
	emit.String(w.BinWriter, "transfer")
	emit.AppCall(w.BinWriter, token.Hash, false)
	emit.Opcode(w.BinWriter, opcode.THROWIFNOT)

	var gas util.Fixed8
	if gasString := ctx.String("gas"); gasString != "" {
		gas, err = util.Fixed8FromString(gasString)
		if err != nil {
			return cli.NewExitError(fmt.Errorf("invalid GAS amount: %v", err), 1)
		}
	}

	tx := transaction.NewInvocationTX(w.Bytes(), gas)
	tx.Attributes = append(tx.Attributes, transaction.Attribute{
		Usage: transaction.Script,
		Data:  from.BytesBE(),
	})

	if err := request.AddInputsAndUnspentsToTx(tx, fromFlag.String(), core.UtilityTokenID(), gas, c); err != nil {
		return cli.NewExitError(fmt.Errorf("can't add GAS to a tx: %v", err), 1)
	}

	if pass, err := readPassword("Password > "); err != nil {
		return cli.NewExitError(err, 1)
	} else if err := acc.Decrypt(pass); err != nil {
		return cli.NewExitError(err, 1)
	} else if err := acc.SignTx(tx); err != nil {
		return cli.NewExitError(fmt.Errorf("can't sign tx: %v", err), 1)
	}

	if err := c.SendRawTransaction(tx); err != nil {
		return cli.NewExitError(err, 1)
	}

	fmt.Println(tx.Hash())
	return nil
}
