import { useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { useT } from "./i18n";
import { Icon } from "./icons";

export function ChipsInput({ values, onChange, placeholder, id, describedBy, mono = true }: { values: string[]; onChange: (v: string[]) => void; placeholder?: string; id?: string; describedBy?: string; mono?: boolean }) {
  const t = useT();
  const [text, setText] = useState("");
  const add = (raw: string) => {
    const parts = raw
      .split(/[\n,]/)
      .map((s) => s.trim())
      .filter(Boolean);
    if (!parts.length) return;
    const next = [...values];
    for (const p of parts) if (!next.includes(p)) next.push(p);
    onChange(next);
    setText("");
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      add(text);
    } else if (e.key === "Backspace" && !text && values.length) {
      onChange(values.slice(0, -1));
    }
  };
  return (
    <div className="col" style={{ gap: 6 }}>
      {values.length > 0 && (
        <div className="row wrap" style={{ gap: 6 }}>
          {values.map((v) => (
            <span key={v} className="chip" style={mono ? undefined : { fontFamily: "var(--sans)" }}>
              <span className="ellipsis">{v}</span>
              <button type="button" aria-label={t("common.removeX", { x: v })} onClick={() => onChange(values.filter((x) => x !== v))}>
                <Icon name="x" size={12} />
              </button>
            </span>
          ))}
        </div>
      )}
      <input
        id={id}
        aria-describedby={describedBy}
        className="input mono"
        value={text}
        placeholder={placeholder}
        onChange={(e) => {
          const v = e.target.value;
          if (/[\n,]/.test(v)) add(v);
          else setText(v);
        }}
        onKeyDown={onKey}
        onBlur={() => text.trim() && add(text)}
      />
    </div>
  );
}

export type KVRows = [string, string][];

export function toRows(m: Record<string, string>): KVRows {
  return Object.entries(m);
}

export function fromRows(rows: KVRows): Record<string, string> {
  const o: Record<string, string> = {};
  for (const [k, v] of rows) if (k.trim()) o[k.trim()] = v;
  return o;
}

export function rowsError(rows: KVRows, t: ReturnType<typeof useT>): string | undefined {
  const seen = new Set<string>();
  for (const [k, v] of rows) {
    const kk = k.trim();
    if (!kk && v.trim()) return t("val.kvEmptyKey");
    if (kk && seen.has(kk)) return t("val.kvDup", { k: kk });
    seen.add(kk);
  }
  return undefined;
}

export function KVEditor({ rows, onChange, keyPh, valPh, label, addLabel }: { rows: KVRows; onChange: (r: KVRows) => void; keyPh: string; valPh: string; label: string; addLabel: string }) {
  const t = useT();
  return (
    <div>
      {rows.map(([k, v], i) => (
        <div className="kv-row" key={i}>
          <input className="input mono" aria-label={`${label} ${i + 1}: ${keyPh}`} placeholder={keyPh} value={k} onChange={(e) => onChange(rows.map((r, j) => (j === i ? [e.target.value, r[1]] : r)))} />
          <input className="input mono" aria-label={`${label} ${i + 1}: ${valPh}`} placeholder={valPh} value={v} onChange={(e) => onChange(rows.map((r, j) => (j === i ? [r[0], e.target.value] : r)))} />
          <button type="button" className="btn ghost icon" aria-label={t("common.removeX", { x: k || String(i + 1) })} onClick={() => onChange(rows.filter((_, j) => j !== i))}>
            <Icon name="trash" size={14} />
          </button>
        </div>
      ))}
      <button type="button" className="btn sm" onClick={() => onChange([...rows, ["", ""]])}>
        <Icon name="plus" size={13} /> {addLabel}
      </button>
    </div>
  );
}

export interface Suggestion {
  value: string;
  hint?: string;
}

export function Autocomplete({ value, onChange, onPick, suggestions, placeholder, id, label, onEnter }: { value: string; onChange: (v: string) => void; onPick?: (v: string) => void; suggestions: Suggestion[]; placeholder?: string; id?: string; label?: string; onEnter?: () => void }) {
  const auto = useId();
  const listId = `${id || auto}-list`;
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const blurTimer = useRef<number>();
  const filtered = useMemo(() => {
    const q = value.trim().toLowerCase();
    const xs = q ? suggestions.filter((s) => s.value.toLowerCase().includes(q)) : suggestions;
    return xs.slice(0, 50);
  }, [value, suggestions]);
  const pick = (v: string) => {
    onChange(v);
    onPick?.(v);
    setOpen(false);
  };
  return (
    <div className="autocomplete grow">
      <input
        id={id || auto}
        className="input mono"
        role="combobox"
        aria-label={label}
        aria-expanded={open && filtered.length > 0}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={open && filtered[active] ? `${listId}-${active}` : undefined}
        autoComplete="off"
        value={value}
        placeholder={placeholder}
        onChange={(e) => {
          onChange(e.target.value);
          setOpen(true);
          setActive(0);
        }}
        onFocus={() => setOpen(true)}
        onBlur={() => {
          blurTimer.current = window.setTimeout(() => setOpen(false), 120);
        }}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") {
            e.preventDefault();
            setOpen(true);
            setActive((a) => Math.min(a + 1, filtered.length - 1));
          } else if (e.key === "ArrowUp") {
            e.preventDefault();
            setActive((a) => Math.max(a - 1, 0));
          } else if (e.key === "Enter") {
            e.preventDefault();
            if (open && filtered[active] && filtered[active].value !== value) pick(filtered[active].value);
            else {
              setOpen(false);
              onEnter?.();
            }
          } else if (e.key === "Escape" && open) {
            e.stopPropagation();
            setOpen(false);
          }
        }}
      />
      {open && filtered.length > 0 && (
        <ul className="ac-list" role="listbox" id={listId}>
          {filtered.map((s, i) => (
            <li
              key={s.value}
              id={`${listId}-${i}`}
              role="option"
              aria-selected={i === active}
              onMouseDown={(e) => {
                e.preventDefault();
                window.clearTimeout(blurTimer.current);
                pick(s.value);
              }}
              onMouseEnter={() => setActive(i)}
            >
              <span className="mono ellipsis">{s.value}</span>
              {s.hint && <span className="owner">{s.hint}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
