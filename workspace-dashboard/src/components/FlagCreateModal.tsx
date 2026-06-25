// workspace-dashboard/src/components/FlagCreateModal.tsx
import { useState } from 'react';
import * as Dialog from '@radix-ui/react-dialog';
import { useForm, useFieldArray } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { motion, AnimatePresence } from 'motion/react';
import { Plus, Trash2, X, ChevronRight, ChevronLeft } from 'lucide-react';
import { API_URL, SDK_TOKEN } from '../config.js';

// ── Zod schema ─────────────────────────────────────────────────────────────
const ruleSchema = z.object({
  attribute: z.string().min(1, 'Attribute required'),
  operator:  z.enum(['equals', 'contains', 'in', 'not_in']),
  value:     z.string().min(1, 'Value required'),
});

const flagSchema = z.object({
  key:         z.string()
    .min(1, 'Key required')
    .regex(/^[a-z0-9][a-z0-9-_]*$/, 'Lowercase, numbers, hyphens, underscores only'),
  name:        z.string().min(1, 'Name required').max(80),
  description: z.string().max(200).optional(),
  flag_type:   z.enum(['boolean', 'string', 'number', 'json']),
  rules:       z.array(ruleSchema),
  rollout_pct: z.number().min(0).max(100),
  enabled:     z.boolean(),
});

type FlagFormData = z.infer<typeof flagSchema>;

// ── API call ────────────────────────────────────────────────────────────────
async function createFlag(data: FlagFormData): Promise<void> {
  const res = await fetch(`${API_URL}/api/v1/flags`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${SDK_TOKEN}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(data),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: 'Unknown error' })) as { message?: string };
    throw new Error(err.message ?? `Create failed: ${res.status}`);
  }
}

// ── Step indicator ──────────────────────────────────────────────────────────
function StepDot({ n, current }: { n: number; current: number }) {
  const done    = n < current;
  const active  = n === current;
  return (
    <div style={{
      width: 28, height: 28, borderRadius: '50%',
      display: 'flex', alignItems: 'center', justifyContent: 'center',
      fontSize: 12, fontWeight: 600,
      background: done || active ? 'var(--color-accent)' : 'var(--color-bg-elevated)',
      color:  done || active ? '#07080d' : 'var(--color-fg-subtle)',
      border: `1px solid ${done || active ? 'var(--color-accent)' : 'var(--color-border)'}`,
      transition: 'all 0.2s',
    }}>
      {done ? '✓' : n}
    </div>
  );
}

// ── Field helpers ───────────────────────────────────────────────────────────
function Field({ label, error, children }: { label: string; error?: string; children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <label style={{ fontSize: 12, fontWeight: 500, color: 'var(--color-fg-muted)' }}>{label}</label>
      {children}
      {error && <span style={{ fontSize: 11, color: 'var(--color-risk-high)' }}>{error}</span>}
    </div>
  );
}

function TextInput({ error, ...props }: React.InputHTMLAttributes<HTMLInputElement> & { error?: string }) {
  return (
    <input
      {...props}
      style={{
        background: 'var(--color-bg-base)', border: `1px solid ${error ? 'var(--color-risk-high)' : 'var(--color-border)'}`,
        borderRadius: 8, padding: '8px 12px', fontSize: 13, color: 'var(--color-fg)',
        outline: 'none', width: '100%',
      }}
      onFocus={e => { e.currentTarget.style.borderColor = 'var(--color-accent)'; }}
      onBlur={e => { e.currentTarget.style.borderColor = error ? 'var(--color-risk-high)' : 'var(--color-border)'; }}
    />
  );
}

// ── Main modal ──────────────────────────────────────────────────────────────
interface Props { open: boolean; onClose: () => void; }

const STEPS = ['Identity', 'Rules', 'Rollout'];

export function FlagCreateModal({ open, onClose }: Props) {
  const [step, setStep] = useState(1);
  const queryClient = useQueryClient();

  const { register, control, handleSubmit, watch, trigger, formState: { errors } } = useForm<FlagFormData>({
    resolver: zodResolver(flagSchema),
    defaultValues: {
      flag_type:   'boolean',
      rules:       [],
      rollout_pct: 0,
      enabled:     false,
    },
  });

  const { fields: rules, append: addRule, remove: removeRule } = useFieldArray({ control, name: 'rules' });

  const mutation = useMutation({
    mutationFn: createFlag,
    onSuccess: (_data, variables) => {
      queryClient.invalidateQueries({ queryKey: ['flags'] });
      toast.success('Flag created', { description: variables.key });
      onClose();
      setStep(1);
    },
    onError: (err: Error) => {
      toast.error('Create failed', { description: err.message });
    },
  });

  const onSubmit = handleSubmit(data => mutation.mutate(data));

  // Per-step field sets — only validate the fields visible on the current step
  const STEP_FIELDS: Record<number, (keyof FlagFormData)[]> = {
    1: ['key', 'name', 'description', 'flag_type'],
    2: ['rules'],
  };

  async function handleNext() {
    const fields = STEP_FIELDS[step];
    const valid = fields ? await trigger(fields) : true;
    if (valid) setStep(s => s + 1);
  }

  return (
    <Dialog.Root open={open} onOpenChange={v => { if (!v) { onClose(); setStep(1); } }}>
      <Dialog.Portal>
        <Dialog.Overlay
          style={{
            position: 'fixed', inset: 0, zIndex: 50,
            background: 'rgba(0,0,0,0.6)',
            backdropFilter: 'blur(2px)',
          }}
        />
        <Dialog.Content
          style={{
            position: 'fixed', zIndex: 51,
            top: '50%', left: '50%',
            transform: 'translate(-50%, -50%)',
            width: 540, maxWidth: 'calc(100vw - 32px)',
            background: 'var(--color-bg-elevated)',
            border: '1px solid var(--color-border-strong)',
            borderRadius: 16,
            boxShadow: 'var(--glow-accent), 0 24px 48px rgba(0,0,0,0.6)',
            outline: 'none',
          }}
        >
          {/* Header */}
          <div style={{ padding: '20px 24px 16px', borderBottom: '1px solid var(--color-border)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
            <Dialog.Title style={{ fontSize: 16, fontWeight: 700, color: 'var(--color-fg)', margin: 0 }}>
              Create Feature Flag
            </Dialog.Title>
            <Dialog.Close style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--color-fg-subtle)', display: 'flex' }}>
              <X size={16} />
            </Dialog.Close>
          </div>

          {/* Step indicators */}
          <div style={{ padding: '16px 24px', display: 'flex', alignItems: 'center', gap: 12, borderBottom: '1px solid var(--color-border)' }}>
            {STEPS.map((label, i) => (
              <div key={label} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <StepDot n={i + 1} current={step} />
                <span style={{ fontSize: 12, color: step === i + 1 ? 'var(--color-fg)' : 'var(--color-fg-subtle)' }}>{label}</span>
                {i < STEPS.length - 1 && <div style={{ width: 24, height: 1, background: 'var(--color-border)' }} />}
              </div>
            ))}
          </div>

          {/* Form body */}
          <form onSubmit={onSubmit}>
            <div style={{ padding: 24, minHeight: 280 }}>
              <AnimatePresence mode="wait">
                {step === 1 && (
                  <motion.div key="step1" initial={{ opacity: 0, x: 20 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: -20 }} transition={{ duration: 0.15 }}>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
                      <Field label="Flag Key *" error={errors.key?.message}>
                        <TextInput placeholder="my-feature-flag" error={errors.key?.message} {...register('key')} />
                      </Field>
                      <Field label="Display Name *" error={errors.name?.message}>
                        <TextInput placeholder="My Feature Flag" error={errors.name?.message} {...register('name')} />
                      </Field>
                      <Field label="Description">
                        <textarea
                          {...register('description')}
                          placeholder="What does this flag control?"
                          style={{
                            background: 'var(--color-bg-base)', border: '1px solid var(--color-border)',
                            borderRadius: 8, padding: '8px 12px', fontSize: 13, color: 'var(--color-fg)',
                            outline: 'none', resize: 'vertical', minHeight: 72, fontFamily: 'inherit',
                          }}
                        />
                      </Field>
                      <Field label="Flag Type">
                        <select {...register('flag_type')} style={{
                          background: 'var(--color-bg-base)', border: '1px solid var(--color-border)',
                          borderRadius: 8, padding: '8px 12px', fontSize: 13, color: 'var(--color-fg)',
                          outline: 'none', cursor: 'pointer',
                        }}>
                          {['boolean', 'string', 'number', 'json'].map(t => (
                            <option key={t} value={t}>{t}</option>
                          ))}
                        </select>
                      </Field>
                    </div>
                  </motion.div>
                )}

                {step === 2 && (
                  <motion.div key="step2" initial={{ opacity: 0, x: 20 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: -20 }} transition={{ duration: 0.15 }}>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
                      <p style={{ fontSize: 13, color: 'var(--color-fg-muted)', margin: '0 0 8px' }}>
                        Add targeting rules to control which users see this flag. Leave empty to target everyone.
                      </p>
                      {rules.map((rule, i) => (
                        <div key={rule.id} style={{ display: 'grid', gridTemplateColumns: '1fr 120px 1fr 32px', gap: 8, alignItems: 'start' }}>
                          <TextInput placeholder="attribute" error={errors.rules?.[i]?.attribute?.message} {...register(`rules.${i}.attribute`)} />
                          <select {...register(`rules.${i}.operator`)} style={{
                            background: 'var(--color-bg-base)', border: '1px solid var(--color-border)',
                            borderRadius: 8, padding: '8px 8px', fontSize: 12, color: 'var(--color-fg)', outline: 'none',
                          }}>
                            {['equals', 'contains', 'in', 'not_in'].map(op => <option key={op} value={op}>{op}</option>)}
                          </select>
                          <TextInput placeholder="value" error={errors.rules?.[i]?.value?.message} {...register(`rules.${i}.value`)} />
                          <button type="button" onClick={() => removeRule(i)} style={{
                            width: 32, height: 32, borderRadius: 8, border: '1px solid var(--color-border)',
                            background: 'transparent', color: 'var(--color-risk-high)', cursor: 'pointer', display: 'flex', alignItems: 'center', justifyContent: 'center',
                          }}>
                            <Trash2 size={13} />
                          </button>
                        </div>
                      ))}
                      <button type="button" onClick={() => addRule({ attribute: '', operator: 'equals', value: '' })} style={{
                        display: 'flex', alignItems: 'center', gap: 6, padding: '8px 12px',
                        borderRadius: 8, border: '1px dashed var(--color-border)',
                        background: 'transparent', color: 'var(--color-accent)', fontSize: 13, cursor: 'pointer',
                      }}>
                        <Plus size={14} /> Add Rule
                      </button>
                    </div>
                  </motion.div>
                )}

                {step === 3 && (
                  <motion.div key="step3" initial={{ opacity: 0, x: 20 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: -20 }} transition={{ duration: 0.15 }}>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
                      <Field label={`Rollout: ${watch('rollout_pct')}%`}>
                        <input type="range" min={0} max={100} step={5} {...register('rollout_pct', { valueAsNumber: true })}
                          style={{ width: '100%', accentColor: 'var(--color-accent)' }} />
                        <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11, color: 'var(--color-fg-subtle)' }}>
                          <span>0%</span><span>50%</span><span>100%</span>
                        </div>
                      </Field>

                      <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer' }}>
                        <input type="checkbox" {...register('enabled')} style={{ accentColor: 'var(--color-accent)', width: 16, height: 16 }} />
                        <div>
                          <div style={{ fontSize: 13, fontWeight: 500, color: 'var(--color-fg)' }}>Enable immediately</div>
                          <div style={{ fontSize: 11, color: 'var(--color-fg-subtle)' }}>Flag will be active after creation</div>
                        </div>
                      </label>

                      {/* Review summary */}
                      <div style={{ background: 'var(--color-bg-surface)', border: '1px solid var(--color-border)', borderRadius: 10, padding: 16 }}>
                        <div style={{ fontSize: 12, fontWeight: 600, color: 'var(--color-fg-muted)', marginBottom: 10, textTransform: 'uppercase', letterSpacing: '0.06em' }}>Summary</div>
                        {[
                          ['Key', watch('key') || '—'],
                          ['Name', watch('name') || '—'],
                          ['Type', watch('flag_type')],
                          ['Rules', `${rules.length} rule${rules.length !== 1 ? 's' : ''}`],
                          ['Rollout', `${watch('rollout_pct')}%`],
                          ['Status', watch('enabled') ? 'Enabled' : 'Disabled'],
                        ].map(([k, v]) => (
                          <div key={k} style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12, padding: '4px 0', borderBottom: '1px solid var(--color-border)' }}>
                            <span style={{ color: 'var(--color-fg-subtle)' }}>{k}</span>
                            <span style={{ color: 'var(--color-fg)', fontFamily: k === 'Key' ? 'var(--font-mono)' : 'inherit' }}>{v}</span>
                          </div>
                        ))}
                      </div>
                    </div>
                  </motion.div>
                )}
              </AnimatePresence>
            </div>

            {/* Footer nav */}
            <div style={{ padding: '16px 24px', borderTop: '1px solid var(--color-border)', display: 'flex', justifyContent: 'space-between' }}>
              <button
                type="button"
                onClick={() => step > 1 ? setStep(s => s - 1) : onClose()}
                style={{
                  display: 'flex', alignItems: 'center', gap: 6, padding: '8px 16px',
                  borderRadius: 8, border: '1px solid var(--color-border)',
                  background: 'transparent', color: 'var(--color-fg-muted)', fontSize: 13, cursor: 'pointer',
                }}
              >
                <ChevronLeft size={14} />
                {step === 1 ? 'Cancel' : 'Back'}
              </button>

              {step < 3 ? (
                <button
                  type="button"
                  onClick={handleNext}
                  style={{
                    display: 'flex', alignItems: 'center', gap: 6, padding: '8px 18px',
                    borderRadius: 8, border: 'none',
                    background: 'var(--color-accent)', color: '#07080d', fontSize: 13, fontWeight: 600, cursor: 'pointer',
                  }}
                >
                  Next <ChevronRight size={14} />
                </button>
              ) : (
                <button
                  type="submit"
                  disabled={mutation.isPending}
                  style={{
                    display: 'flex', alignItems: 'center', gap: 6, padding: '8px 18px',
                    borderRadius: 8, border: 'none',
                    background: mutation.isPending ? 'var(--color-border)' : 'var(--color-accent)',
                    color: '#07080d', fontSize: 13, fontWeight: 600,
                    cursor: mutation.isPending ? 'not-allowed' : 'pointer',
                  }}
                >
                  {mutation.isPending ? 'Creating…' : 'Create Flag'}
                </button>
              )}
            </div>
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
