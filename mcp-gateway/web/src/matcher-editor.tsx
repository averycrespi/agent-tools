import type { MatcherSchemaSuggestions } from "./matcher-catalog";
import { FormField } from "./primitives";

const jsonNumber = /^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$/;
export type MatcherScalarType = "null" | "boolean" | "string" | "number";
export interface MatcherAtom {
  operator: "equals" | "regex";
  pointer: string;
  // Retain the equality type while MATCHES displays String.
  type: MatcherScalarType;
  explicitType?: boolean;
  value: string;
}

export function validMatcherPointer(pointer: string): boolean {
  return (
    pointer.startsWith("/") &&
    new TextEncoder().encode(pointer).length <= 256 &&
    !/~(?:[^01]|$)/.test(pointer)
  );
}

export function matcherConstraintText(atoms: readonly MatcherAtom[]): string {
  if (atoms.length === 0) return "null";
  const members = (operator: MatcherAtom["operator"]) =>
    atoms
      .filter((atom) => atom.operator === operator)
      .map((atom) => {
        let value: string;
        if (operator === "regex") value = JSON.stringify(atom.value);
        else if (atom.type === "null") value = "null";
        else if (atom.type === "boolean") {
          if (atom.value !== "true" && atom.value !== "false")
            throw new Error("Boolean values must be true or false.");
          value = atom.value;
        } else if (atom.type === "number") {
          if (!jsonNumber.test(atom.value))
            throw new Error("Number values must use valid JSON number syntax.");
          value = atom.value;
        } else value = JSON.stringify(atom.value);
        return `${JSON.stringify(atom.pointer)}:${value}`;
      });
  const equalities = members("equals");
  const expressions = members("regex");
  const equalsMember =
    equalities.length === 0 ? "" : `,"equals":{${equalities.join(",")}}`;
  return `{"version":2${equalsMember},"regex":{${expressions.join(",")}}}`;
}

export function MatcherAtomEditor({
  idPrefix,
  testPrefix,
  addTestID,
  atoms,
  suggestions,
  schemaState = "unavailable",
  disabled = false,
  onChange,
}: {
  idPrefix: string;
  testPrefix: string;
  addTestID?: string;
  atoms: readonly MatcherAtom[];
  suggestions?: MatcherSchemaSuggestions | undefined;
  schemaState?: "loading" | "unavailable";
  disabled?: boolean;
  onChange: (atoms: MatcherAtom[]) => void;
}) {
  const update = (index: number, patch: Partial<MatcherAtom>) =>
    onChange(
      atoms.map((atom, position) =>
        position === index ? { ...atom, ...patch } : atom,
      ),
    );
  const pointerOptions = `${idPrefix}-pointer-options`;
  let preview = "null";
  try {
    preview = matcherConstraintText(atoms);
  } catch {
    preview =
      "Complete each matcher value to preview the serialized constraint.";
  }
  return (
    <div class="matcher-editor">
      <p class="field-hint">
        All constraints must match. Schema guidance supplies defaults, not
        restrictions.
      </p>
      <datalist id={pointerOptions}>
        {(suggestions?.fields ?? []).map((field) => (
          <option
            value={field.pointer}
            label={`${field.type}${field.values.length === 0 ? "" : ` · ${field.values.join(", ")}`}${field.description === null ? "" : ` · ${field.description}`}`}
            key={field.pointer}
          />
        ))}
      </datalist>
      {atoms.map((atom, index) => {
        const field = suggestions?.fields.find(
          (candidate) => candidate.pointer === atom.pointer,
        );
        const regex = atom.operator === "regex";
        const status =
          suggestions === undefined
            ? schemaState === "loading"
              ? "Loading…"
              : "Unavailable"
            : field === undefined
              ? "Unknown"
              : "Known";
        const guidance = `${
          field === undefined
            ? "Enter a custom RFC 6901 pointer or choose a schema suggestion."
            : `${field.type}${field.description === null ? "" : ` · ${field.description}`}.`
        }${
          regex
            ? " MATCHES requires String: full-string Go RE2, no coercion."
            : " EQUALS compares the selected scalar type without coercion."
        }${
          field !== undefined && field.type !== (regex ? "string" : atom.type)
            ? regex
              ? ` Schema suggests ${field.type}; only string runtime values can match.`
              : ` Schema suggests ${field.type}; your ${atom.type} type is retained.`
            : ""
        }`;
        const hintID = `${idPrefix}-guidance-${index}`;
        const valuesID = `${idPrefix}-values-${index}`;
        const values = !regex && field?.type === atom.type ? field.values : [];
        return (
          <div
            class="matcher-row"
            data-testid={`${testPrefix}-atom`}
            key={index}
          >
            <FormField id={`${idPrefix}-pointer-${index}`} label="JSON pointer">
              {(attributes) => (
                <input
                  {...attributes}
                  aria-describedby={hintID}
                  data-testid={`${testPrefix}-pointer`}
                  value={atom.pointer}
                  list={pointerOptions}
                  autocomplete="off"
                  disabled={disabled}
                  onInput={(event) => {
                    const pointer = event.currentTarget.value;
                    const suggestion = suggestions?.fields.find(
                      (candidate) => candidate.pointer === pointer,
                    );
                    update(index, {
                      pointer,
                      ...(!atom.explicitType &&
                      atom.value === "" &&
                      suggestion !== undefined
                        ? { type: suggestion.type }
                        : {}),
                    });
                  }}
                />
              )}
            </FormField>
            <span
              class="matcher-status"
              data-testid={`${testPrefix}-status`}
              aria-label={`Path recognition: ${status}`}
            >
              {status}
            </span>
            <FormField id={`${idPrefix}-operator-${index}`} label="Operator">
              {(attributes) => (
                <select
                  {...attributes}
                  aria-describedby={hintID}
                  data-testid={`${testPrefix}-operator`}
                  value={atom.operator}
                  disabled={disabled}
                  onChange={(event) =>
                    update(index, {
                      operator: event.currentTarget
                        .value as MatcherAtom["operator"],
                      value: "",
                    })
                  }
                >
                  <option value="equals">EQUALS</option>
                  <option value="regex">MATCHES</option>
                </select>
              )}
            </FormField>
            <FormField id={`${idPrefix}-type-${index}`} label="Scalar type">
              {(attributes) => (
                <select
                  {...attributes}
                  aria-describedby={hintID}
                  data-testid={`${testPrefix}-type`}
                  value={regex ? "string" : atom.type}
                  disabled={disabled || regex}
                  onChange={(event) =>
                    update(index, {
                      type: event.currentTarget.value as MatcherScalarType,
                      explicitType: true,
                    })
                  }
                >
                  <option value="null">Null</option>
                  <option value="boolean">Boolean</option>
                  <option value="string">String</option>
                  <option value="number">Number</option>
                </select>
              )}
            </FormField>
            <FormField
              id={`${idPrefix}-value-${index}`}
              label={regex ? "RE2 pattern" : "Scalar value"}
            >
              {(attributes) =>
                !regex && atom.type === "boolean" ? (
                  <select
                    {...attributes}
                    aria-describedby={hintID}
                    data-testid={`${testPrefix}-value`}
                    value={atom.value}
                    disabled={disabled}
                    onChange={(event) =>
                      update(index, { value: event.currentTarget.value })
                    }
                  >
                    <option value="">Choose a boolean</option>
                    <option value="true">true</option>
                    <option value="false">false</option>
                  </select>
                ) : !regex && atom.type === "null" ? (
                  <input
                    {...attributes}
                    aria-describedby={hintID}
                    value="null"
                    disabled
                  />
                ) : (
                  <input
                    {...attributes}
                    aria-describedby={hintID}
                    data-testid={`${testPrefix}-value`}
                    value={atom.value}
                    list={valuesID}
                    autocomplete="off"
                    disabled={disabled}
                    onInput={(event) =>
                      update(index, { value: event.currentTarget.value })
                    }
                  />
                )
              }
            </FormField>
            <datalist id={valuesID}>
              {values.map((value) => (
                <option value={value} key={value} />
              ))}
            </datalist>
            <button
              class="matcher-remove"
              type="button"
              aria-label={`Remove constraint ${index + 1}`}
              disabled={disabled}
              onClick={() =>
                onChange(atoms.filter((_, position) => position !== index))
              }
            >
              Remove
            </button>
            <p id={hintID} class="field-hint matcher-guidance">
              {guidance}
            </p>
          </div>
        );
      })}
      <button
        data-testid={addTestID ?? `${testPrefix}-add`}
        type="button"
        disabled={disabled || atoms.length >= 16}
        onClick={() =>
          onChange([
            ...atoms,
            { operator: "equals", pointer: "/", type: "string", value: "" },
          ])
        }
      >
        Add constraint
      </button>
      <details class="matcher-disclosure">
        <summary>Read-only serialized constraint · v2</summary>
        <pre class="inert-json" aria-label="Constraint preview" tabindex={0}>
          <code>{preview}</code>
        </pre>
      </details>
    </div>
  );
}
