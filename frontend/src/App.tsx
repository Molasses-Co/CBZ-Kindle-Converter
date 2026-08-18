import { useEffect, useRef, useState } from 'react';
import { Dialogs, Events } from "@wailsio/runtime";
import { CBZService, type CBZProgress } from "../bindings/cbz-converter/pkg/services";
import {
  CheckCircle2, FolderOpen, FolderOutput, Images, Loader2, Play, Settings2, XCircle,
} from "lucide-react";

const DEFAULT_WIDTH = 1080;
const DEFAULT_HEIGHT = 1920;

const KINDLE_PRESETS: { id: string; label: string; width: number; height: number }[] = [
  { id: 'KINDLE_600x800', label: 'Kindle Básico (600×800)', width: 600, height: 800 },
  { id: 'KINDLE_758x1024', label: 'Paperwhite 1 e 2 (758×1024)', width: 758, height: 1024 },
  { id: 'KINDLE_1072x1448', label: 'Paperwhite 3/4, Voyage, Oasis 1 (1072×1448)', width: 1072, height: 1448 },
  { id: 'KINDLE_1236x1648', label: 'Paperwhite 5 (1236×1648)', width: 1236, height: 1648 },
  { id: 'KINDLE_1264x1680', label: 'Paperwhite 6, Oasis 2/3, Colorsoft (1264×1680)', width: 1264, height: 1680 },
  { id: 'KINDLE_1860x2480', label: 'Kindle Scribe (1860×2480)', width: 1860, height: 2480 },
  { id: 'KINDLE_824x1200', label: 'Kindle DX (824×1200)', width: 824, height: 1200 },
];

type FileStatus = 'pending' | 'processing' | 'done' | 'error';
interface QueueItem { name: string; data: string; status: FileStatus; }

const joinPath = (dir: string, name: string) => dir.replace(/[\\/]+$/, '') + '/' + name;

function App() {
  const [width, setWidth] = useState<number>(DEFAULT_WIDTH);
  const [height, setHeight] = useState<number>(DEFAULT_HEIGHT);
  const [presetId, setPresetId] = useState<string>('custom');
  const [queue, setQueue] = useState<QueueItem[]>([]);
  const [outDir, setOutDir] = useState<string>('');
  const [isProcessing, setIsProcessing] = useState<boolean>(false);
  const [current, setCurrent] = useState<number>(-1);
  const [progress, setProgress] = useState<number>(0);
  const [progressStage, setProgressStage] = useState<string>('');
  const [progressMessage, setProgressMessage] = useState<string>('');

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
    if (preset) { setWidth(preset.width); setHeight(preset.height); }
  };

  const readAsBase64 = (file: File) => new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const result = reader.result as string;
      resolve(result.split(',')[1] ?? result);
    };
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });

  const handleFilesChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const list = Array.from(e.target.files ?? []);
    if (list.length === 0) return;
    const items: QueueItem[] = [];
    for (const file of list) {
      try {
        const data = await readAsBase64(file);
        items.push({ name: file.name, data, status: 'pending' });
      } catch {
        showToast(`Falha ao ler ${file.name}.`, "Erro");
      }
    }
    setQueue((q) => [...q, ...items]);
    e.target.value = '';
  };

  const handleChooseDir = async () => {
    const dir = (await Dialogs.OpenFile({
      Title: "Escolher pasta de saída",
      CanChooseDirectories: true,
      CanChooseFiles: false,
    })) as string;
    if (dir) setOutDir(dir);
  };

  const setStatus = (index: number, status: FileStatus) => {
    setQueue((q) => q.map((it, i) => (i === index ? { ...it, status } : it)));
  };

  const handleProcess = async () => {
    if (queue.length === 0) { showToast("Adicione ao menos um arquivo CBZ.", "Aviso"); return; }
    if (!outDir) { showToast("Escolha a pasta de saída.", "Aviso"); return; }
    if (width <= 0 || height <= 0) { showToast("Informe largura e altura maiores que zero.", "Aviso"); return; }

    setIsProcessing(true);
    let done = 0, failed = 0;
    for (let i = 0; i < queue.length; i++) {
      setCurrent(i);
      setProgress(0); setProgressStage(''); setProgressMessage('');
      setStatus(i, 'processing');
      try {
        const processed = await CBZService.ProcessCBZ(queue[i].name, queue[i].data, width, height);
        if (!processed) throw new Error("sem saída do backend");
        await CBZService.SaveCBZ(joinPath(outDir, queue[i].name), processed);
        setStatus(i, 'done'); done++;
      } catch (err) {
        console.error(err);
        setStatus(i, 'error'); failed++;
      }
    }
    setCurrent(-1);
    setIsProcessing(false);
    showToast(`${done} concluído(s), ${failed} falha(s).`, "Concluído");
  };

  const canProcess = queue.length > 0 && outDir !== '' && width > 0 && height > 0 && !isProcessing;
  const doneCount = queue.filter((q) => q.status === 'done').length;

  return (
    <>
      <main className="container">
        <header className="brand">
          <h1 className="title">CBZ <span className="title-accent">Studio</span></h1>
          <p className="subtitle">Redimensione suas HQs em lote para o Kindle — selecione os arquivos, a pasta de saída e processe um a um.</p>
        </header>

        <section className="panel">
          {/* Resolução */}
          <div className="group">
            <span className="group-label">Resolução</span>
            <div className="input-box">
              <Settings2 className="input-icon" aria-hidden="true" />
              <select className="input" aria-label="Preset de resolução" value={presetId} onChange={handlePresetChange}>
                <option value="custom">Personalizado</option>
                {KINDLE_PRESETS.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
              </select>
            </div>
            <div className="row">
              <div className="input-box">
                <input className="input" type="number" aria-label="Largura" placeholder="Largura (px)" value={width} min={1}
                  onChange={(e) => { setWidth(Number(e.target.value)); setPresetId('custom'); }} />
              </div>
              <div className="input-box">
                <input className="input" type="number" aria-label="Altura" placeholder="Altura (px)" value={height} min={1}
                  onChange={(e) => { setHeight(Number(e.target.value)); setPresetId('custom'); }} />
              </div>
            </div>
          </div>

          {/* Arquivos */}
          <div className="group">
            <span className="group-label">Arquivos CBZ</span>
            <label className="dropzone">
              <Images size={20} aria-hidden="true" />
              <span>Adicionar CBZ…</span>
              <input type="file" accept=".cbz,application/zip" multiple onChange={handleFilesChange} />
            </label>
            {queue.length > 0 && (
              <ul className="file-list">
                {queue.map((f, i) => (
                  <li key={`${f.name}-${i}`} className={`file-item is-${f.status}${i === current ? ' is-current' : ''}`}>
                    <span className="file-name" title={f.name}>{f.name}</span>
                    <span className="file-status">
                      {f.status === 'processing' && <Loader2 size={14} className="animate-spin" aria-hidden="true" />}
                      {f.status === 'done' && <CheckCircle2 size={14} aria-hidden="true" />}
                      {f.status === 'error' && <XCircle size={14} aria-hidden="true" />}
                      {f.status === 'pending' && <span className="dot-pending" />}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>

          {/* Pasta de saída */}
          <div className="group">
            <span className="group-label">Pasta de saída</span>
            <button className="input-box folder-btn" onClick={handleChooseDir} type="button">
              <FolderOpen className="input-icon" aria-hidden="true" />
              <span className={`folder-path${outDir ? '' : ' is-empty'}`}>{outDir || 'Escolher pasta…'}</span>
            </button>
          </div>

          {/* Ação */}
          <button className="btn btn-primary" disabled={!canProcess} onClick={handleProcess}>
            {isProcessing
              ? <><Loader2 className="animate-spin" size={17} aria-hidden="true" /> Processando {current + 1}/{queue.length}…</>
              : <><Play size={17} aria-hidden="true" /> Processar {queue.length > 0 ? `${queue.length} arquivo(s)` : ''}</>}
          </button>

          {(isProcessing || doneCount > 0) && (
            <div className="progress-box" role="status" aria-live="polite">
              <div className="progress-head">
                <span className="progress-stage">{isProcessing ? (progressStage || 'Iniciando') : `${doneCount}/${queue.length} concluído(s)`}</span>
                <span className="progress-pct">{isProcessing ? `${progress}%` : ''}</span>
              </div>
              <div className="progress-track"><div className="progress-bar" style={{ width: isProcessing ? `${progress}%` : `${(doneCount / Math.max(queue.length, 1)) * 100}%` }} /></div>
              {isProcessing && <span className="progress-msg">{progressMessage}</span>}
            </div>
          )}
        </section>

        <footer className="footer">
          <span className="footer-version"><FolderOutput size={14} aria-hidden="true" /> CBZ Studio</span>
        </footer>
      </main>

      <div className="toast" ref={toastRef} role="status" aria-live="polite">
        <span className="toast-label" ref={toastLabelRef}>Status</span>
        <span aria-label="result" className="toast-msg" ref={toastMsgRef}></span>
      </div>
    </>
  );
}

export default App;
