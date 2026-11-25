package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/kauavnogueira/hookbox/internal/replay"
	"github.com/kauavnogueira/hookbox/internal/server"
	"github.com/kauavnogueira/hookbox/internal/storage"
)

const version = "1.2.0"

func printUsage() {
	fmt.Println(`Hookbox - Gravador, Inspector e Replay de Webhooks Locais

Uso:
  hookbox listen [--port 8080] [--file webhooks.db] [--forward <url>]
      Sobe o servidor local para receber webhooks em /webhook/*,
      grava no banco SQLite/JSON e disponibiliza a Interface Web em http://localhost:8080.

  hookbox list [--limit 20] [--method POST] [--path /stripe]
      Exibe uma tabela com os webhooks gravados, com suporte a filtros.

  hookbox show <id> [--file webhooks.db]
      Exibe todos os detalhes de um webhook: headers, payload formatado e cURL pronto.

  hookbox replay <id> --to <target_url> [opções]
      Reenvia uma cópia do webhook salvo para a sua API local ou remota,
      com suporte a re-assinatura HMAC, customização de payload e headers.

  hookbox clean [--file webhooks.db] [--force]
      Limpa todo o histórico de webhooks gravados.

  hookbox ui [--port 8080] [--file webhooks.db]
      Abre a interface web preta minimalista para inspecionar webhooks no navegador.

Opções do comando 'listen':
  --port <porta>           Porta HTTP local (padrão: 8080)
  --forward <url>          Encaminha webhooks recebidos em tempo real para a sua API (modo proxy/tee)
  --max-body <MB>          Limite máximo de tamanho do corpo da requisição em MB (padrão: 10)

Opções do comando 'list':
  --limit <n>              Limitar quantidade de registros exibidos
  --method <POST|GET|...>  Filtrar por método HTTP
  --path <texto>           Filtrar por trecho do caminho

Opções do comando 'replay':
  --to <url>               URL de destino obrigatória (ex: http://localhost:3000/api/webhooks)
  --preserve-path          Anexa o caminho original (wh.Path) à URL de destino
  --preserve-query         Anexa os parâmetros de query originais (wh.Query) à URL de destino
  --stripe-secret <whsec>  Recalcula o cabeçalho 'Stripe-Signature' com timestamp atual e HMAC-SHA256
  --github-secret <secret> Recalcula o cabeçalho 'X-Hub-Signature-256' com HMAC-SHA256
  --hmac-secret <secret>   Chave secreta para assinatura HMAC genérica
  --hmac-header <header>   Nome do cabeçalho HMAC genérico (padrão: X-Signature-256)
  --data <novo_corpo>      Sobrescreve o payload original pelo corpo informado
  --header <chave: valor>  Injeta ou substitui cabeçalhos HTTP na requisição

Opções globais:
  --file <caminho>         Arquivo de armazenamento (.db para SQLite ou .json para JSON, padrão: webhooks.db)
  --version                Exibe a versão do Hookbox
  --help                   Exibe esta mensagem de ajuda`)
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "listen":
		runListen(os.Args[2:])
	case "list":
		runList(os.Args[2:])
	case "show", "inspect":
		runShow(os.Args[2:])
	case "replay":
		runReplay(os.Args[2:])
	case "clean", "clear":
		runClean(os.Args[2:])
	case "ui":
		runUI(os.Args[2:])
	case "--version", "-v", "version":
		fmt.Printf("Hookbox v%s\n", version)
	case "--help", "-h", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Comando desconhecido: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func runListen(args []string) {
	fs := flag.NewFlagSet("listen", flag.ExitOnError)
	port := fs.Int("port", 8080, "Porta local para escutar requisições")
	filePath := fs.String("file", "webhooks.db", "Arquivo para armazenar os webhooks (.db ou .json)")
	forward := fs.String("forward", "", "URL de destino para encaminhamento transparente em tempo real")
	maxBody := fs.Int64("max-body", 10, "Limite de tamanho do corpo em MB (padrão: 10)")
	_ = fs.Parse(args)

	store, err := storage.NewStore(*filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao inicializar armazenamento: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	srv := server.NewServerWithOptions(server.ServerOptions{
		Port:         *port,
		Store:        store,
		ForwardURL:   *forward,
		MaxBodyBytes: *maxBody << 20,
	})

	storageType := "SQLite"
	if strings.HasSuffix(strings.ToLower(*filePath), ".json") {
		storageType = "JSON"
	}

	fmt.Println("==================================================")
	fmt.Printf("  📦 Hookbox Recorder & Inspector v%s\n", version)
	fmt.Printf("  📡 Endpoint Webhooks: http://localhost:%d/webhook/*\n", *port)
	fmt.Printf("  🖥️  Interface Web:     http://localhost:%d/\n", *port)
	fmt.Printf("  💾 Armazenamento:     %s (%s)\n", *filePath, storageType)
	if *forward != "" {
		fmt.Printf("  ➔ Forward Ativo:     %s\n", *forward)
	}
	fmt.Println("  Aguardando webhooks... (Pressione Ctrl+C para parar)")
	fmt.Println("==================================================")

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nParando servidor Hookbox...")
		_ = store.Close()
		os.Exit(0)
	}()

	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Erro no servidor: %v\n", err)
		os.Exit(1)
	}
}

func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	filePath := fs.String("file", "webhooks.db", "Arquivo de histórico dos webhooks (.db ou .json)")
	limit := fs.Int("limit", 0, "Número máximo de registros a exibir")
	method := fs.String("method", "", "Filtrar por método HTTP (ex: POST, GET)")
	path := fs.String("path", "", "Filtrar por trecho do caminho (ex: /stripe)")
	_ = fs.Parse(args)

	store, err := storage.NewStore(*filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao abrir histórico: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	filter := storage.ListFilter{
		Limit:  *limit,
		Method: *method,
		Path:   *path,
	}

	list, err := store.GetFiltered(filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao listar webhooks: %v\n", err)
		os.Exit(1)
	}

	if len(list) == 0 {
		fmt.Println("Nenhum webhook encontrado com os critérios informados. Use 'hookbox listen' para gravar.")
		return
	}

	fmt.Printf("\n📦 Webhooks Gravados (%d exibidos) em %s:\n\n", len(list), *filePath)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tMÉTODO\tCAMINHO\tTAMANHO\tRECEBIDO EM\tQUERY")
	fmt.Fprintln(w, "--\t------\t-------\t-------\t-----------\t-----")

	for _, wh := range list {
		queryDisplay := "-"
		if wh.Query != "" {
			queryDisplay = wh.Query
			if len(queryDisplay) > 25 {
				queryDisplay = queryDisplay[:22] + "..."
			}
		}

		timeStr := wh.Timestamp.Local().Format("2006-01-02 15:04:05")
		sizeStr := fmt.Sprintf("%d B", wh.BodyBytes)

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			wh.ID,
			wh.Method,
			wh.Path,
			sizeStr,
			timeStr,
			queryDisplay,
		)
	}
	w.Flush()
	fmt.Printf("\n💡 Dica: use 'hookbox show <id>' para ver os headers e payload completo.\n\n")
}

func runShow(args []string) {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	filePath := fs.String("file", "webhooks.db", "Arquivo de histórico dos webhooks")

	var id string
	var parseArgs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			parseArgs = append(parseArgs, arg)
			if (arg == "--file" || arg == "-file") && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
				parseArgs = append(parseArgs, args[i])
			}
		} else if id == "" {
			id = arg
		} else {
			parseArgs = append(parseArgs, arg)
		}
	}
	_ = fs.Parse(parseArgs)

	if id == "" {
		fmt.Fprintln(os.Stderr, "Erro: informe o ID do webhook a ser inspecionado.")
		fmt.Fprintln(os.Stderr, "Exemplo: hookbox show req_01")
		os.Exit(1)
	}

	store, err := storage.NewStore(*filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao abrir histórico: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	wh, err := store.GetByID(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("================================================================================")
	fmt.Printf("📦 Webhook [%s] - %s %s\n", wh.ID, wh.Method, wh.Path)
	fmt.Println("================================================================================")
	fmt.Printf("Data/Hora:    %s\n", wh.Timestamp.Local().Format("2006-01-02 15:04:05 MST"))
	fmt.Printf("Tamanho:      %d bytes\n", wh.BodyBytes)
	if wh.Query != "" {
		fmt.Printf("Query Params: %s\n", wh.Query)
	}
	if wh.Host != "" {
		fmt.Printf("Host:         %s\n", wh.Host)
	}
	if wh.RemoteAddr != "" {
		fmt.Printf("Origem (IP):  %s\n", wh.RemoteAddr)
	}

	fmt.Println("\n--- Cabeçalhos (Headers) ---")
	if len(wh.Headers) == 0 {
		fmt.Println("  (Nenhum cabeçalho salvo)")
	} else {
		for k, values := range wh.Headers {
			for _, v := range values {
				fmt.Printf("  %s: %s\n", k, v)
			}
		}
	}

	fmt.Println("\n--- Corpo da Requisição (Payload) ---")
	if wh.Body == "" {
		fmt.Println("  (Corpo vazio)")
	} else {
		var prettyJSON bytes.Buffer
		if err := json.Indent(&prettyJSON, []byte(wh.Body), "  ", "  "); err == nil {
			fmt.Printf("  %s\n", prettyJSON.String())
		} else {
			fmt.Printf("  %s\n", wh.Body)
		}
	}

	fmt.Println("\n--- Exemplo de Replay via cURL ---")
	curlCmd := fmt.Sprintf("curl -X %s \"http://localhost:3000%s\"", wh.Method, wh.Path)
	for k, values := range wh.Headers {
		if strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range values {
			curlCmd += fmt.Sprintf(" \\\n  -H \"%s: %s\"", k, v)
		}
	}
	if wh.Body != "" {
		curlCmd += fmt.Sprintf(" \\\n  -d '%s'", strings.ReplaceAll(wh.Body, "'", "'\\''"))
	}
	fmt.Printf("%s\n\n", curlCmd)
}

func runClean(args []string) {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	filePath := fs.String("file", "webhooks.db", "Arquivo de histórico dos webhooks")
	force := fs.Bool("force", false, "Limpar sem confirmação interativa")
	fs.BoolVar(force, "f", false, "Limpar sem confirmação interativa (atalho)")
	_ = fs.Parse(args)

	store, err := storage.NewStore(*filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao abrir histórico: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	count, _ := store.Count()
	if count == 0 {
		fmt.Println("O histórico já está vazio.")
		return
	}

	if !*force {
		fmt.Printf("Tem certeza que deseja apagar todos os %d webhooks gravados em %s? (s/N): ", count, *filePath)
		var confirm string
		_, _ = fmt.Scanln(&confirm)
		confirm = strings.ToLower(strings.TrimSpace(confirm))
		if confirm != "s" && confirm != "sim" && confirm != "y" && confirm != "yes" {
			fmt.Println("Operação cancelada.")
			return
		}
	}

	if err := store.Clear(); err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao limpar histórico: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Histórico de %s limpo com sucesso! (%d registros removidos)\n", *filePath, count)
}

func runUI(args []string) {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	port := fs.Int("port", 8080, "Porta local para abrir a interface web")
	filePath := fs.String("file", "webhooks.db", "Arquivo de histórico dos webhooks")
	_ = fs.Parse(args)

	store, err := storage.NewStore(*filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao abrir histórico: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	srv := server.NewServer(*port, store)

	fmt.Println("==================================================")
	fmt.Printf("  📦 Hookbox Web Inspector v%s\n", version)
	fmt.Printf("  🖥️  Interface Web: http://localhost:%d\n", *port)
	fmt.Printf("  💾 Banco de dados: %s\n", *filePath)
	fmt.Println("  Pressione Ctrl+C para encerrar")
	fmt.Println("==================================================")

	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Erro no servidor da interface: %v\n", err)
		os.Exit(1)
	}
}

func runReplay(args []string) {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	to := fs.String("to", "", "URL de destino para o replay (ex: http://localhost:3000/api/webhooks)")
	filePath := fs.String("file", "webhooks.db", "Arquivo de histórico dos webhooks (.db ou .json)")

	// Options: Re-signing
	stripeSecret := fs.String("stripe-secret", "", "Chave secreta Stripe (whsec_...) para recalcular Stripe-Signature com timestamp atual")
	githubSecret := fs.String("github-secret", "", "Chave secreta GitHub para recalcular X-Hub-Signature-256")
	hmacSecret := fs.String("hmac-secret", "", "Chave secreta para assinatura HMAC-SHA256 genérica")
	hmacHeader := fs.String("hmac-header", "X-Signature-256", "Nome do cabeçalho para assinatura HMAC genérica")

	// Options: Routing
	preservePath := fs.Bool("preserve-path", false, "Anexa o caminho original recebido à URL de destino")
	preserveQuery := fs.Bool("preserve-query", false, "Anexa os parâmetros de query originais à URL de destino")

	// Options: Customization
	data := fs.String("data", "", "Sobrescrever o corpo (payload) do webhook no replay")
	fs.StringVar(data, "body", "", "Sobrescrever o corpo (payload) do webhook (alias)")
	customHeader := fs.String("header", "", "Injeta ou substitui cabeçalho no formato 'Chave: Valor'")

	flagsWithVal := map[string]bool{
		"--to": true, "-to": true,
		"--file": true, "-file": true,
		"--stripe-secret": true, "-stripe-secret": true,
		"--github-secret": true, "-github-secret": true,
		"--hmac-secret": true, "-hmac-secret": true,
		"--hmac-header": true, "-hmac-header": true,
		"--data": true, "-data": true,
		"--body": true, "-body": true,
		"--header": true, "-header": true,
	}

	// Parse arguments robustly regardless of flag order
	var id string
	var parseArgs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if len(arg) > 0 && arg[0] == '-' {
			parseArgs = append(parseArgs, arg)
			if flagsWithVal[arg] && !strings.Contains(arg, "=") {
				if i+1 < len(args) {
					i++
					parseArgs = append(parseArgs, args[i])
				}
			}
		} else if id == "" {
			id = arg
		} else {
			parseArgs = append(parseArgs, arg)
		}
	}

	_ = fs.Parse(parseArgs)

	if id == "" {
		fmt.Fprintln(os.Stderr, "Erro: você precisa informar o ID do webhook a ser reenviado.")
		fmt.Fprintln(os.Stderr, "Exemplo: hookbox replay req_01 --to http://localhost:3000/api/webhooks")
		os.Exit(1)
	}

	if *to == "" {
		fmt.Fprintln(os.Stderr, "Erro: a flag --to com a URL de destino é obrigatória.")
		fmt.Fprintln(os.Stderr, "Exemplo: hookbox replay req_01 --to http://localhost:3000/api/webhooks")
		os.Exit(1)
	}

	targetURL, err := url.ParseRequestURI(*to)
	if err != nil || targetURL.Scheme == "" || targetURL.Host == "" {
		fmt.Fprintf(os.Stderr, "Erro: URL de destino inválida: %q\n", *to)
		os.Exit(1)
	}

	store, err := storage.NewStore(*filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao abrir histórico: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	wh, err := store.GetByID(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro: %v\n", err)
		os.Exit(1)
	}

	customHeadersMap := make(map[string]string)
	if *customHeader != "" {
		parts := strings.SplitN(*customHeader, ":", 2)
		if len(parts) == 2 {
			customHeadersMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}

	replayOpts := replay.ReplayOptions{
		StripeSecret:  *stripeSecret,
		GitHubSecret:  *githubSecret,
		HMACSecret:    *hmacSecret,
		HMACHeader:    *hmacHeader,
		PreservePath:  *preservePath,
		PreserveQuery: *preserveQuery,
		CustomBody:    *data,
		CustomHeaders: customHeadersMap,
	}

	resolvedURL, err := replay.ResolveTargetURL(*to, wh, replayOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao calcular URL de destino: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n🔁 Disparando Replay de [%s]...\n", wh.ID)
	fmt.Printf("   Origem gravada: %s %s (%d bytes)\n", wh.Method, wh.Path, wh.BodyBytes)
	fmt.Printf("   Destino final:  %s\n", resolvedURL)

	if *stripeSecret != "" {
		fmt.Println("   🔐 Re-assinatura: Stripe (Stripe-Signature atualizado com timestamp atual)")
	}
	if *githubSecret != "" {
		fmt.Println("   🔐 Re-assinatura: GitHub (X-Hub-Signature-256 HMAC-SHA256)")
	}
	if *hmacSecret != "" {
		fmt.Printf("   🔐 Re-assinatura: Genérica HMAC (%s)\n", *hmacHeader)
	}
	if *data != "" {
		fmt.Printf("   ✏️  Payload customizado (%d bytes)\n", len(*data))
	}
	if len(customHeadersMap) > 0 {
		fmt.Printf("   📑 Cabeçalhos customizados injetados: %d\n", len(customHeadersMap))
	}

	sender := replay.NewSender(30 * time.Second)
	result, err := sender.SendWithOptions(wh, *to, replayOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Falha no replay: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n✅ Replay enviado com sucesso!")
	fmt.Printf("   Status: %s\n", result.StatusText)
	fmt.Printf("   Latência: %v\n", result.Duration.Round(time.Millisecond))
	if len(result.ResponseBody) > 0 {
		preview := result.ResponseBody
		if len(preview) > 500 {
			preview = preview[:500] + "... (truncado)"
		}
		fmt.Printf("   Resposta: %s\n", preview)
	}
	fmt.Println()
}
