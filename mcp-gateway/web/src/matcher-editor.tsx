import type { MatcherSchemaSuggestions } from "./matcher-catalog";
import { FormField, InertJSON } from "./primitives";

const jsonNumber = /^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$/;
export type MatcherScalarType = "null" | "boolean" | "string" | "number";
export interface MatcherAtom {
  operator: "equals" | "regex";
  pointer: string;
  type: MatcherScalarType;
  value: string;
}

export function validMatcherPointer(pointer: string): boolean {
  return (
    pointer.startsWith("/") &&
    new TextEncoder().encode(pointer).length <= 256 &&
    !/~(?:[^01]|$)/.test(pointer)
  );
}

export function matcherConstraintText(
  atoms: readonly MatcherAtom[],
  forceVersion2 = false,
): string {
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
  if (expressions.length === 0 && !forceVersion2)
    return `{"equals":{${equalities.join(",")}}}`;
  const equalsMember =
    equalities.length === 0 ? "" : `,"equals":{${equalities.join(",")}}`;
  return `{"version":2${equalsMember},"regex":{${expressions.join(",")}}}`;
}

function preview(atoms: readonly MatcherAtom[]): unknown {
  if (atoms.length === 0) return null;
  const equals: Record<string, unknown> = {};
  const regex: Record<string, string> = {};
  for (const atom of atoms) {
    if (atom.operator === "regex") regex[atom.pointer] = atom.value;
    else
      equals[atom.pointer] =
        atom.type === "null"
          ? null
          : atom.type === "boolean"
            ? atom.value === "true"
            : atom.value;
  }
  return Object.keys(regex).length === 0
    ? { equals }
    : {
        version: 2,
        ...(Object.keys(equals).length === 0 ? {} : { equals }),
        regex,
      };
}

export function MatcherAtomEditor({
  idPrefix,
  testPrefix,
  addTestID,
  atoms,
  suggestions,
  disabled = false,
  onChange,
}: {
  idPrefix: string;
  testPrefix: string;
  addTestID?: string;
  atoms: readonly MatcherAtom[];
  suggestions?: MatcherSchemaSuggestions | undefined;
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
  return (
    <>
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
        return (
          <div class="form-grid" data-testid={`${testPrefix}-atom`} key={index}>
            <FormField id={`${idPrefix}-operator-${index}`} label="Operator">
              {(attributes) => (
                <select
                  {...attributes}
                  data-testid={`${testPrefix}-operator`}
                  value={atom.operator}
                  disabled={disabled}
                  onChange={(event) =>
                    update(index, {
                      operator: event.currentTarget
                        .value as MatcherAtom["operator"],
                      type:
                        event.currentTarget.value === "regex"
                          ? "string"
                          : atom.type,
                    })
                  }
                >
                  <option value="equals">Equals</option>
                  <option value="regex">Full-string RE2</option>
                </select>
              )}
            </FormField>
            <FormField
              id={`${idPrefix}-pointer-${index}`}
              label="JSON pointer"
              hint={
                field === undefined
                  ? "Enter a custom RFC 6901 pointer or choose a schema suggestion."
                  : `${field.type}${field.regexAvailable ? " · regex available" : " · equality only"}${field.values.length === 0 ? "" : ` · allowed: ${field.values.join(", ")}`}${field.description === null ? "" : ` · ${field.description}`}`
              }
            >
              {(attributes) => (
                <input
                  {...attributes}
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
                      ...(atom.operator === "equals" && suggestion !== undefined
                        ? { type: suggestion.type }
                        : {}),
                    });
                  }}
                />
              )}
            </FormField>
            {atom.operator === "equals" && (
              <FormField id={`${idPrefix}-type-${index}`} label="Scalar type">
                {(attributes) => (
                  <select
                    {...attributes}
                    data-testid={`${testPrefix}-type`}
                    value={atom.type}
                    disabled={disabled}
                    onChange={(event) =>
                      update(index, {
                        type: event.currentTarget.value as MatcherScalarType,
                      })
                    }
                  >
                    <option value="null">null</option>
                    <option value="boolean">boolean</option>
                    <option value="string">string</option>
                    <option value="number">number</option>
                  </select>
                )}
              </FormField>
            )}
            {(atom.operator === "regex" || atom.type !== "null") && (
              <FormField
                id={`${idPrefix}-value-${index}`}
                label={
                  atom.operator === "regex" ? "RE2 pattern" : "Scalar value"
                }
                hint={
                  atom.operator === "regex"
                    ? "The pattern must match the complete string value."
                    : "The scalar is compared without coercion."
                }
              >
                {(attributes) => (
                  <input
                    {...attributes}
                    data-testid={`${testPrefix}-value`}
                    value={atom.value}
                    disabled={disabled}
                    onInput={(event) =>
                      update(index, { value: event.currentTarget.value })
                    }
                  />
                )}
              </FormField>
            )}
            <button
              type="button"
              disabled={disabled}
              onClick={() =>
                onChange(atoms.filter((_, position) => position !== index))
              }
            >
              Remove atom
            </button>
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
        Add matcher
      </button>
      <InertJSON value={preview(atoms)} label="Constraint preview" />
    </>
  );
}
