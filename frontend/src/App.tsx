import { useEffect, useRef, useState } from 'react';
import { Dialogs, Events } from "@wailsio/runtime";
import { CBZService, type CBZProgress } from "../bindings/cbz-converter/pkg/services";
import { FileImage, Loader2, Settings2, Upload } from "lucide-react";

const DEFAULT_WIDTH = 1080;
const DEFAULT_HEIGHT = 1920;

// Presets de resolução de dispositivos Kindle (Largura × Altura). Selecionar um
// preset preenche automaticamente os campos de largura/altura.
const KINDLE_PRESETS: { id: string; label: string; width: number; height: number }[] = [
  { id: 'KINDLE_600x800', label: 'Kindle Básico (600×800)', width: 600, height: 800 },
  { id: 'KINDLE_758x1024', label: 'Paperwhite 1 e 2 (758×1024)', width: 758, height: 1024 },
  { id: 'KINDLE_1072x1448', label: 'Paperwhite 3/4, Voyage, Oasis 1 (1072×1448)', width: 1072, height: 1448 },
  { id: 'KINDLE_1236x1648', label: 'Paperwhite 5 (1236×1648)', width: 1236, height: 1648 },
  { id: 'KINDLE_1264x1680', label: 'Paperwhite 6, Oasis 2/3, Colorsoft (1264×1680)', width: 1264, height: 1680 },
  { id: 'KINDLE_1860x2480', label: 'Kindle Scribe (1860×2480)', width: 1860, height: 2480 },
  { id: 'KINDLE_824x1200', label: 'Kindle DX (824×1200)', width: 824, height: 1200 },
];

function App() {
  const [width, setWidth] = useState<number>(DEFAULT_WIDTH);
  const [height, setHeight] = useState<number>(DEFAULT_HEIGHT);
  const [presetId, setPresetId] = useState<string>('custom');
  const [fileName, setFileName] = useState<string>('');
  const [fileData, setFileData] = useState<string | null>(null);
  const [isProcessing, setIsProcessing] = useState<boolean>(false);
  const [progress, setProgress] = useState<number>(0);
  const [progressStage, setProgressStage] = useState<string>('');
  const [progressMessage, setProgressMessage] = useState<string>('');

  // Assina o evento de progresso emitido pelo backend durante o processamento.
  useEffect(() => {
    return Events.On("cbz:progress", (ev) => {
      const d = ev.data as CBZProgress;
      setProgress(Math.round(d.percentage));
      setProgressStage(d.stage);
      setProgressMessage(d.message);
    });
  }, []);

  const toastRef = useRef<HTMLDivElement | null>(null);
  const toastMsgRef = useRef<HTMLSpanElement | null>(null);
  const toastLabelRef = useRef<HTMLSpanElement | null>(null);
  const toastTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const showToast = (message: string, label = "Status") => {
    if (toastLabelRef.current) toastLabelRef.current.innerText = label;
    if (toastMsgRef.current) toastMsgRef.current.innerText = message;
    if (toastRef.current) toastRef.current.classList.add('is-visible');
    clearTimeout(toastTimer.current);
    toastTimer.current = setTimeout(() => {
      if (toastRef.current) toastRef.current.classList.remove('is-visible');
    }, 4000);
  };

  const handlePresetChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    const id = e.target.value;
    setPresetId(id);
    if (id === 'custom') return;
    const preset = KINDLE_PRESETS.find((p) => p.id === id);
    if (preset) {
      setWidth(preset.width);
      setHeight(preset.height);
    }
  };

  const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) {
      setFileName('');
      setFileData(null);
      return;
    }

    const reader = new FileReader();
    reader.onload = () => {
      const result = reader.result as string;
      const base64 = result.split(',')[1] ?? result;
      setFileName(file.name);
      setFileData(base64);
    };
    reader.onerror = () => {
      showToast("Falha ao ler o arquivo.", "Erro");
    };
    reader.readAsDataURL(file);
  };

  const handleProcess = async () => {
    if (!fileData || !fileName) {
      showToast("Selecione um arquivo CBZ primeiro.", "Aviso");
      return;
    }
    if (width <= 0 || height <= 0) {
      showToast("Informe largura e altura maiores que zero.", "Aviso");
      return;
    }

    setIsProcessing(true);
    setProgress(0);
    setProgressStage('');
    setProgressMessage('');
    try {
      // 1. Backend processa (redimensiona) e devolve o CBZ processado
      const processed = await CBZService.ProcessCBZ(fileName, fileData, width, height);
      if (!processed) {
        showToast("O backend não retornou o CBZ processado.", "Erro");
        return;
      }

      // 2. Usuário escolhe onde salvar
      const savePath = await Dialogs.SaveFile({
        Title: "Salvar arquivo CBZ",
        Filename: fileName.replace(/\.cbz$/i, '') + "_processado.cbz",
        Filters: [{ DisplayName: "Comic Book Zip (*.cbz)", Pattern: "*.cbz" }],
      });

      if (!savePath) {
        showToast("Salvamento cancelado.", "Aviso");
        return;
      }

      // 3. Backend grava o arquivo no caminho escolhido
      await CBZService.SaveCBZ(savePath, processed);
      showToast(`CBZ salvo em: ${savePath}`, "Concluído");
    } catch (err) {
      console.error(err);
      showToast("Erro ao processar as imagens.", "Erro");
    } finally {
      setIsProcessing(false);
    }
  };

  const hasFile = fileName.length > 0 && fileData !== null;
  const isButtonEnabled = hasFile && width > 0 && height > 0 && !isProcessing;

  return (
    <>
      <main className="container">
        <header className="brand">
          <h1 className="title">Ajuste e gere seu <span className="title-accent">CBZ</span></h1>
          <p className="subtitle">
            Informe a resolução das páginas, selecione o arquivo CBZ e clique em processar.
          </p>
        </header>

        <section className="greet" style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
          {/* Presets de resolução */}
          <div className="input-box">
            <Settings2 className="input-icon" aria-hidden="true" />
            <select
              className="input"
              aria-label="Preset de resolução"
              value={presetId}
              onChange={handlePresetChange}
              style={{ width: '100%', appearance: 'auto' }}
            >
              <option value="custom">Personalizado</option>
              {KINDLE_PRESETS.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.label}
                </option>
              ))}
            </select>
          </div>

          {/* Resolução */}
          <div style={{ display: 'flex', gap: '0.6rem' }}>
            <div className="input-box">
              <input
                className="input"
                type="number"
                aria-label="Largura"
                placeholder="Largura (px)"
                value={width}
                min={1}
                onChange={(e) => { setWidth(Number(e.target.value)); setPresetId('custom'); }}
              />
            </div>
            <div className="input-box">
              <input
                className="input"
                type="number"
                aria-label="Altura"
                placeholder="Altura (px)"
                value={height}
                min={1}
                onChange={(e) => { setHeight(Number(e.target.value)); setPresetId('custom'); }}
              />
            </div>
          </div>

          {/* Upload */}
          <div className="input-box">
            <Upload className="input-icon" aria-hidden="true" />
            <input
              className="input"
              type="file"
              accept=".cbz,application/zip"
              aria-label="Arquivo CBZ"
              onChange={handleFileChange}
            />
          </div>

          {hasFile ? (
            <div className="badge" style={{ alignSelf: 'flex-start', display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
              <FileImage size={14} aria-hidden="true" />
              {fileName}
            </div>
          ) : (
            <p className="subtitle" style={{ fontSize: '0.9rem', margin: 0 }}>
              Nenhum arquivo selecionado.
            </p>
          )}

          {/* Processar */}
          <button
            className="btn"
            disabled={!isButtonEnabled}
            onClick={handleProcess}
            style={{
              opacity: isButtonEnabled ? 1 : 0.5,
              cursor: isButtonEnabled ? 'pointer' : 'not-allowed',
              justifyContent: 'center',
              alignSelf: 'flex-start',
            }}
          >
            {isProcessing ? (
              <>
                <Loader2 className="animate-spin" size={17} aria-hidden="true" />
                Processando... {progress}%
              </>
            ) : (
              'Processar e Salvar CBZ'
            )}
          </button>

          {/* Progresso do processamento */}
          {isProcessing && (
            <div className="progress-box" role="status" aria-live="polite">
              <div className="progress-head">
                <span className="progress-stage">{progressStage || 'Iniciando'}</span>
                <span className="progress-pct">{progress}%</span>
              </div>
              <div className="progress-track">
                <div className="progress-bar" style={{ width: `${progress}%` }} />
              </div>
              <span className="progress-msg">{progressMessage}</span>
            </div>
          )}
        </section>
      </main>

      {/* Toast Notification */}
      <div className="toast" ref={toastRef} role="status" aria-live="polite">
        <span className="toast-label" ref={toastLabelRef}>Status</span>
        <span aria-label="result" className="toast-msg" ref={toastMsgRef}></span>
      </div>
    </>
  );
}

export default App;