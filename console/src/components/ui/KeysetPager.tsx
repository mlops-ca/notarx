import { Pagination } from "antd";
import { CONSOLE_PAGE_SIZE_OPTIONS } from "./useConsolePagination";

/**
 * The pager of a keyset listing (ADR 0023 section 8). The server never says how
 * many rows exist, so the pager only knows the pages read so far and whether
 * the server offered another one: while a next cursor exists it reports one row
 * more than the pages up to the current one hold.
 */
export function KeysetPager({
  page,
  pageSize,
  itemCount,
  hasNext,
  onChange,
  disabled,
  pageSizeOptions = CONSOLE_PAGE_SIZE_OPTIONS,
}: {
  page: number;
  pageSize: number;
  itemCount: number;
  hasNext: boolean;
  onChange: (page: number, pageSize: number) => void;
  disabled?: boolean;
  pageSizeOptions?: number[];
}) {
  return (
    <div className="flex justify-end px-4 py-3">
      <Pagination
        current={page}
        pageSize={pageSize}
        total={
          hasNext ? page * pageSize + 1 : (page - 1) * pageSize + itemCount
        }
        pageSizeOptions={pageSizeOptions}
        showSizeChanger
        disabled={disabled}
        onChange={onChange}
      />
    </div>
  );
}
