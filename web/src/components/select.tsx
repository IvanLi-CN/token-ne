import * as SelectPrimitive from "@radix-ui/react-select";
import { Check, ChevronDown } from "lucide-react";
import { forwardRef } from "react";

export interface SelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

// Radix rejects an empty item value, so the empty option travels under a
// sentinel and is mapped back on the way out.
const EMPTY = "__empty__";

export interface SelectProps {
  value?: string;
  defaultValue?: string;
  onValueChange: (value: string) => void;
  options: SelectOption[];
  placeholder?: string;
  label: string;
  disabled?: boolean;
  className?: string;
  id?: string;
  "aria-describedby"?: string;
}

/**
 * The one select control of the console. Native <select> elements are not used:
 * they cannot match the themed trigger and popup the rest of the UI library
 * provides.
 */
export const Select = forwardRef<HTMLButtonElement, SelectProps>(function Select(
  {
    value,
    defaultValue,
    onValueChange,
    options,
    placeholder,
    label,
    disabled,
    className = "",
    id,
    "aria-describedby": describedBy,
  },
  ref,
) {
  const encode = (raw: string) => (raw === "" ? EMPTY : raw);
  const decode = (raw: string) => (raw === EMPTY ? "" : raw);
  return (
    <SelectPrimitive.Root
      value={value === undefined ? undefined : encode(value)}
      defaultValue={defaultValue === undefined ? undefined : encode(defaultValue)}
      onValueChange={(next) => onValueChange(decode(next))}
      disabled={disabled}
    >
      <SelectPrimitive.Trigger
        ref={ref}
        id={id}
        aria-label={label}
        aria-describedby={describedBy}
        className={`input select-trigger ${className}`.trim()}
      >
        <SelectPrimitive.Value placeholder={placeholder ?? "请选择"} />
        <SelectPrimitive.Icon className="select-icon">
          <ChevronDown size={16} aria-hidden="true" />
        </SelectPrimitive.Icon>
      </SelectPrimitive.Trigger>
      <SelectPrimitive.Portal>
        <SelectPrimitive.Content position="popper" sideOffset={4} className="select-content">
          <SelectPrimitive.Viewport>
            {options.map((option) => (
              <SelectPrimitive.Item
                key={option.value}
                value={encode(option.value)}
                disabled={option.disabled}
                className="select-item"
              >
                <SelectPrimitive.ItemText>{option.label}</SelectPrimitive.ItemText>
                <SelectPrimitive.ItemIndicator className="select-indicator">
                  <Check size={14} aria-hidden="true" />
                </SelectPrimitive.ItemIndicator>
              </SelectPrimitive.Item>
            ))}
          </SelectPrimitive.Viewport>
        </SelectPrimitive.Content>
      </SelectPrimitive.Portal>
    </SelectPrimitive.Root>
  );
});
